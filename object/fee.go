// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package object

import "sync/atomic"

// fees reads a vendor's fee: the percent a model resold from that vendor bills over
// the price the vendor states ("5.5"), as the host's operator sets it.
var fees atomic.Pointer[func(vendor string) string]

// SetFees installs where a vendor's fee is read (nil clears it). The host reads its
// operator's setting there; ai keeps no fee of its own.
func SetFees(f func(vendor string) string) {
	if f == nil {
		fees.Store(nil)
		return
	}
	fees.Store(&f)
}

// Fee is a vendor's fee in percent as the host sets it, "" when no host sets one: a
// model resold from that vendor then bills at the vendor's price.
func Fee(vendor string) string {
	if f := fees.Load(); f != nil {
		return (*f)(vendor)
	}
	return ""
}
