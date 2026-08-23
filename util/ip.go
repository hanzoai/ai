// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/ai/conf"
)

var isLocalIpDb bool

// tryInitLocalDb tries to initialize the local IP database from different paths
func tryInitLocalDb() error {
	err := Init("data/17monipdb.dat")
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		err = Init("../data/17monipdb.dat")
	}
	return err
}

// InitIpDb initializes the IP database based on configuration
func InitIpDb() {
	isLocalIpDb = conf.AppConfig.DefaultBool("isLocalIpDb", false)
	if isLocalIpDb {
		// Use local IP database
		err := tryInitLocalDb()
		if err != nil {
			panic(err)
		}
	} else {
		// Try MaxMind first
		if err := InitMaxmindDb(); err != nil {
			if !MaxmindDownloadInProgress {
				// Try 17monipdb as fallback
				err = tryInitLocalDb()
				if err != nil {
					panic(err)
				}
			}
		}
	}
}

func GetInfoFromIP(ip string) (*LocationInfo, error) {
	if !IsInternetIp(ip) {
		return &LocationInfo{}, nil
	}

	var info *LocationInfo
	var err error
	if isLocalIpDb {
		info, err = Find(ip)
	} else {
		info, err = FindMaxmind(ip)
	}
	if err != nil {
		return nil, err
	}

	return info, nil
}

// GetDescFromIP returns a string description of an IP address
func GetDescFromIP(ip string) string {
	info, err := GetInfoFromIP(ip)
	if err != nil {
		return ""
	}

	res := info.Country + ", " + info.Region + ", " + info.City
	if info.Isp != Null {
		res += ", " + info.Isp
	}

	return res
}

func GetIPInfo(clientIP string) string {
	if clientIP == "" {
		return ""
	}

	ips := strings.Split(clientIP, ",")
	res := ""
	for i := range ips {
		ip := strings.TrimSpace(ips[i])
		// desc := GetDescFromIP(ip)
		ipstr := fmt.Sprintf("%s: %s", ip, "")
		if i != len(ips)-1 {
			res += ipstr + " -> "
		} else {
			res += ipstr
		}
	}

	return res
}
