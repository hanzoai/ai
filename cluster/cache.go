// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
)

type cachedMetrics struct {
	TotalCPU         resource.Quantity `json:"totalCPU"`
	TotalMemory      resource.Quantity `json:"totalMemory"`
	PodCount         int               `json:"podCount"`
	CPUPercentage    float64           `json:"cpuPercentage"`
	MemoryPercentage float64           `json:"memoryPercentage"`
	LastUpdated      time.Time         `json:"lastUpdated"`
}

// eventCache represents namespace-level event cache
type eventCache struct {
	Namespace string               `json:"namespace"` // Namespace name
	Events    map[string]*v1.Event `json:"events"`    // Event name -> Event object
}
type cacheManager struct {
	factory      informers.SharedInformerFactory
	nsInformer   coreinformers.NamespaceInformer
	svcCache     map[string]map[string]*v1.Service           // namespace -> name -> service
	deployCache  map[string]map[string]*appsv1.Deployment    // namespace -> name -> deployment
	ingressCache map[string]map[string]*networkingv1.Ingress // namespace -> name -> ingress
	eventCaches  map[string]*eventCache                      // namespace -> eventCache
	nsCache      map[string]*v1.Namespace                    // name -> namespace
	nodeCache    map[string]*v1.Node                         // name -> node
	metricsCache map[string]*cachedMetrics                   // namespace -> metrics
	mu           sync.RWMutex
	stopCh       chan struct{}
	started      bool
	// debug counters
	svcHits     int
	deployHits  int
	nodeHits    int
	nsHits      int
	ingressHits int
	eventHits   int
	metricsHits int
}

var (
	mirror     *cacheManager
	cachedHost string
)

// initMirror initializes the cache manager
func initMirror() error {
	if mirror != nil {
		return nil
	}
	factory := informers.NewSharedInformerFactory(client.clientSet, 30*time.Second)
	mgr := &cacheManager{
		factory:      factory,
		nsInformer:   factory.Core().V1().Namespaces(),
		svcCache:     make(map[string]map[string]*v1.Service),
		deployCache:  make(map[string]map[string]*appsv1.Deployment),
		ingressCache: make(map[string]map[string]*networkingv1.Ingress),
		eventCaches:  make(map[string]*eventCache),
		nsCache:      make(map[string]*v1.Namespace),
		nodeCache:    make(map[string]*v1.Node),
		metricsCache: make(map[string]*cachedMetrics),
		stopCh:       make(chan struct{}),
	}
	if err := mgr.setupInformers(); err != nil {
		return fmt.Errorf("failed to setup informers: %v", err)
	}
	mirror = mgr
	return nil
}

// startMirror starts the cache manager if it exists
func startMirror(lang string) error {
	if mirror == nil {
		return fmt.Errorf("cache manager not initialized")
	}
	if mirror.started {
		return nil
	}
	if err := mirror.Start(lang); err != nil {
		return fmt.Errorf("failed to start cache manager: %v", err)
	}
	return nil
}

// setupInformers configures all required informers
func (cm *cacheManager) setupInformers() error {
	// Namespace informer
	cm.nsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cm.onNamespaceAdd,
		UpdateFunc: cm.onNamespaceUpdate,
		DeleteFunc: cm.onNamespaceDelete,
	})
	// Service informer for all namespaces
	svcInformer := cm.factory.Core().V1().Services()
	svcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cm.onServiceAdd,
		UpdateFunc: cm.onServiceUpdate,
		DeleteFunc: cm.onServiceDelete,
	})
	// Deployment informer for all namespaces
	deployInformer := cm.factory.Apps().V1().Deployments()
	deployInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cm.onDeploymentAdd,
		UpdateFunc: cm.onDeploymentUpdate,
		DeleteFunc: cm.onDeploymentDelete,
	})
	// Node informer for external IP resolution
	nodeInformer := cm.factory.Core().V1().Nodes()
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cm.onNodeAdd,
		UpdateFunc: cm.onNodeUpdate,
		DeleteFunc: cm.onNodeDelete,
	})
	// Ingress informer for all namespaces
	ingressInformer := cm.factory.Networking().V1().Ingresses()
	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cm.onIngressAdd,
		UpdateFunc: cm.onIngressUpdate,
		DeleteFunc: cm.onIngressDelete,
	})
	// Event informer
	eventInformer := cm.factory.Core().V1().Events()
	eventInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    cm.onEventAdd,
		UpdateFunc: cm.onEventUpdate,
		DeleteFunc: cm.onEventDelete,
	})
	return nil
}

// Event-related event handlers
func (cm *cacheManager) onEventAdd(obj interface{}) {
	event := obj.(*v1.Event)
	cm.updateEventCache(event)
}

func (cm *cacheManager) onEventUpdate(oldObj, newObj interface{}) {
	event := newObj.(*v1.Event)
	cm.updateEventCache(event)
}

func (cm *cacheManager) onEventDelete(obj interface{}) {
	event := obj.(*v1.Event)
	cm.deleteEventCache(event)
}

// updateEventCache updates the event cache
func (cm *cacheManager) updateEventCache(event *v1.Event) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	ns := event.Namespace
	if cm.eventCaches[ns] == nil {
		cm.eventCaches[ns] = &eventCache{
			Namespace: ns,
			Events:    make(map[string]*v1.Event),
		}
	}
	cm.eventCaches[ns].Events[event.Name] = event
}

// deleteEventCache deletes from event cache
func (cm *cacheManager) deleteEventCache(event *v1.Event) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if nsEventCache, exists := cm.eventCaches[event.Namespace]; exists {
		delete(nsEventCache.Events, event.Name)
		if len(nsEventCache.Events) == 0 {
			delete(cm.eventCaches, event.Namespace)
		}
	}
}

// getEvents retrieves all events for the specified namespace
func (cm *cacheManager) getEvents(namespace string) []*v1.Event {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.eventHits++
	var events []*v1.Event
	if nsEventCache, exists := cm.eventCaches[namespace]; exists {
		for _, event := range nsEventCache.Events {
			events = append(events, event)
		}
	}
	return events
}

// Start begins the cache manager
func (cm *cacheManager) Start(lang string) error {
	if cm.started {
		return nil
	}
	cm.factory.Start(cm.stopCh)
	// Wait for initial sync
	synced := cm.factory.WaitForCacheSync(cm.stopCh)
	for informer, synced := range synced {
		if !synced {
			return fmt.Errorf("failed to sync informer: %v", informer)
		}
	}
	if metricsClient != nil {
		go cm.startMetricsPoller(60*time.Second, lang)
	}
	cm.started = true
	return nil
}

// Stop stops the cache manager
func (cm *cacheManager) Stop() {
	if cm.started {
		close(cm.stopCh)
		cm.started = false
	}
}

func (cm *cacheManager) startMetricsPoller(interval time.Duration, lang string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	cm.updateAllMetrics(lang)
	for {
		select {
		case <-ticker.C:
			cm.updateAllMetrics(lang)
		case <-cm.stopCh:
			return
		}
	}
}

func (cm *cacheManager) updateAllMetrics(lang string) {
	cm.mu.RLock()
	namespaces := make([]string, 0, len(cm.nsCache))
	for name := range cm.nsCache {
		namespaces = append(namespaces, name)
	}
	cm.mu.RUnlock()
	for _, namespace := range namespaces {
		cm.updateNamespaceMetrics(namespace, lang)
		time.Sleep(100 * time.Millisecond)
	}
}

// updateNamespaceMetrics updates cached metrics for a namespace
func (cm *cacheManager) updateNamespaceMetrics(namespace string, lang string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	metrics, err := calculateNamespaceMetrics(ctx, metricsClient, namespace, cm.deployCache, &cm.mu, lang)
	if err != nil {
		return
	}
	cm.mu.Lock()
	cm.metricsCache[namespace] = metrics
	cm.mu.Unlock()
}

func (cm *cacheManager) getNamespaceMetricsFromCache(namespace string) (*cachedMetrics, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.metricsHits++
	metrics, found := cm.metricsCache[namespace]
	return metrics, found
}

// Namespace handlers
func (cm *cacheManager) onNamespaceAdd(obj interface{}) {
	ns := obj.(*v1.Namespace)
	cm.updateNamespaceCache(ns)
}

func (cm *cacheManager) onNamespaceUpdate(oldObj, newObj interface{}) {
	ns := newObj.(*v1.Namespace)
	cm.updateNamespaceCache(ns)
}

func (cm *cacheManager) onNamespaceDelete(obj interface{}) {
	ns := obj.(*v1.Namespace)
	cm.deleteNamespaceCache(ns)
}

func (cm *cacheManager) updateNamespaceCache(ns *v1.Namespace) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.nsCache[ns.Name] = ns
}

func (cm *cacheManager) deleteNamespaceCache(ns *v1.Namespace) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.nsCache, ns.Name)
	delete(cm.metricsCache, ns.Name)
	delete(cm.eventCaches, ns.Name)
}

// Service event handlers
func (cm *cacheManager) onServiceAdd(obj interface{}) {
	svc := obj.(*v1.Service)
	cm.updateServiceCache(svc)
}

func (cm *cacheManager) onServiceUpdate(oldObj, newObj interface{}) {
	svc := newObj.(*v1.Service)
	cm.updateServiceCache(svc)
}

func (cm *cacheManager) onServiceDelete(obj interface{}) {
	svc := obj.(*v1.Service)
	cm.deleteServiceCache(svc)
}

func (cm *cacheManager) updateServiceCache(svc *v1.Service) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	ns := svc.Namespace
	if cm.svcCache[ns] == nil {
		cm.svcCache[ns] = make(map[string]*v1.Service)
	}
	cm.svcCache[ns][svc.Name] = svc
}

func (cm *cacheManager) deleteServiceCache(svc *v1.Service) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if nsCache, exists := cm.svcCache[svc.Namespace]; exists {
		delete(nsCache, svc.Name)
	}
}

// Deployment event handlers
func (cm *cacheManager) onDeploymentAdd(obj interface{}) {
	deploy := obj.(*appsv1.Deployment)
	cm.updateDeploymentCache(deploy)
}

func (cm *cacheManager) onDeploymentUpdate(oldObj, newObj interface{}) {
	deploy := newObj.(*appsv1.Deployment)
	cm.updateDeploymentCache(deploy)
}

func (cm *cacheManager) onDeploymentDelete(obj interface{}) {
	deploy := obj.(*appsv1.Deployment)
	cm.deleteDeploymentCache(deploy)
}

func (cm *cacheManager) updateDeploymentCache(deploy *appsv1.Deployment) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	ns := deploy.Namespace
	if cm.deployCache[ns] == nil {
		cm.deployCache[ns] = make(map[string]*appsv1.Deployment)
	}
	cm.deployCache[ns][deploy.Name] = deploy
}

func (cm *cacheManager) deleteDeploymentCache(deploy *appsv1.Deployment) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if nsCache, exists := cm.deployCache[deploy.Namespace]; exists {
		delete(nsCache, deploy.Name)
	}
}

// Node event handlers
func (cm *cacheManager) onNodeAdd(obj interface{}) {
	node := obj.(*v1.Node)
	cm.updateNodeCache(node)
}

func (cm *cacheManager) onNodeUpdate(oldObj, newObj interface{}) {
	node := newObj.(*v1.Node)
	cm.updateNodeCache(node)
}

func (cm *cacheManager) onNodeDelete(obj interface{}) {
	node := obj.(*v1.Node)
	cm.deleteNodeCache(node)
}

func (cm *cacheManager) updateNodeCache(node *v1.Node) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.nodeCache[node.Name] = node
}

func (cm *cacheManager) deleteNodeCache(node *v1.Node) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.nodeCache, node.Name)
}

func (cm *cacheManager) onIngressAdd(obj interface{}) {
	ingress := obj.(*networkingv1.Ingress)
	cm.updateIngressCache(ingress)
}

func (cm *cacheManager) onIngressUpdate(oldObj, newObj interface{}) {
	ingress := newObj.(*networkingv1.Ingress)
	cm.updateIngressCache(ingress)
}

func (cm *cacheManager) onIngressDelete(obj interface{}) {
	ingress := obj.(*networkingv1.Ingress)
	cm.deleteIngressCache(ingress)
}

func (cm *cacheManager) updateIngressCache(ingress *networkingv1.Ingress) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	ns := ingress.Namespace
	if cm.ingressCache[ns] == nil {
		cm.ingressCache[ns] = make(map[string]*networkingv1.Ingress)
	}
	cm.ingressCache[ns][ingress.Name] = ingress
}

func (cm *cacheManager) deleteIngressCache(ingress *networkingv1.Ingress) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if nsCache, exists := cm.ingressCache[ingress.Namespace]; exists {
		delete(nsCache, ingress.Name)
	}
}

func (cm *cacheManager) getNamespace(name string) *v1.Namespace {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.nsHits++
	if ns, exists := cm.nsCache[name]; exists {
		return ns
	}
	return nil
}

// Cache access methods with hit counting
func (cm *cacheManager) getServices(namespace string) []*v1.Service {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.svcHits++
	var services []*v1.Service
	if nsCache, exists := cm.svcCache[namespace]; exists {
		for _, svc := range nsCache {
			if svc.Name != "kubernetes" { // Skip default service
				services = append(services, svc)
			}
		}
	}
	return services
}

func (cm *cacheManager) getDeployments(namespace string) []*appsv1.Deployment {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.deployHits++
	var deployments []*appsv1.Deployment
	if nsCache, exists := cm.deployCache[namespace]; exists {
		for _, deploy := range nsCache {
			deployments = append(deployments, deploy)
		}
	}
	return deployments
}

func (cm *cacheManager) getNodes() []*v1.Node {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.nodeHits++
	var nodes []*v1.Node
	for _, node := range cm.nodeCache {
		nodes = append(nodes, node)
	}
	return nodes
}

func (cm *cacheManager) getIngresses(namespace string) []*networkingv1.Ingress {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	cm.ingressHits++
	var ingresses []*networkingv1.Ingress
	if nsCache, exists := cm.ingressCache[namespace]; exists {
		for _, ingress := range nsCache {
			ingresses = append(ingresses, ingress)
		}
	}
	return ingresses
}
