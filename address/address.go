// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Package address names the caller's address on the wire between a host in front
// and this module.
//
// ONE SYMBOL, BECAUSE THE TWO SIDES ARE WRITTEN IN TWO REPOSITORIES. The host that
// states the address is hanzoai/cloud; the lane that believes it is
// controllers.publicAddr here. While each spelled the name itself there were two
// constants held equal by nothing but attention, and the failure that arrangement
// invites is not a compile error — the stamp lands under one name, the read looks
// under another, finds nothing, and falls back to the socket peer, which behind an
// in-cluster ingress is one value for everyone. That is the defect the const below
// records, arriving a second time by a different door. A single symbol cannot
// diverge from itself, so the two sides now agree by construction rather than by
// care, and the name can be changed in one edit.
//
// A PACKAGE OF ITS OWN, because the obvious home is the expensive one. This lived
// in object, and object links 1122 packages; the host that stamps loads subsystems
// rather than containing them, so reaching into object for a string would have
// pulled the whole model layer into the light host. A name has no dependencies —
// this package imports nothing at all — so it sits where both sides may reach it
// and neither pays for the other.
//
// The same reasoning already put funding outside internal/: a seam the host writes
// to and this module reads from has to be reachable across the module boundary.
package address

// Header carries the caller's address from a host in front to this module.
//
// A SUBSYSTEM CANNOT SEE THE CONNECTION. Run as its own process behind a host that
// reaches it over a unix socket, the request arriving here has the SOCKET as its
// peer — empty, and the same for everyone. Anything derived from it is therefore one
// value for every caller on earth, which is not a degraded answer but a single
// answer wearing every caller's name.
//
// It cost a live defect once: the public lane keys its per-visitor ceiling on the
// caller's address, and one address for everyone made that ceiling one bucket for
// the whole internet. So the host answers, because the host is where the connection
// is, and it says so under this name.
//
// A HEADER rather than a function to install, because a header is a VALUE and it is
// what survives the crossing — the same journey Authorization already makes into
// these controllers. The value also arrives the same way whether the host is a
// process in front or an edge, so there is one thing to read and no second mode.
//
// Forgery is answered by the host always OVERWRITING it, and here by never believing
// it from a caller who reached us directly: publicAddr takes it only when the peer is
// one of our own. A stranger's own claim about their address is not evidence.
//
// THE NAME STATES THE FACT AND NOT THE VENDOR. The host that stamps this fronts Lux,
// Zoo and Pars as well as Hanzo, so a name carrying one of those brands was wrong on
// the other three and said nothing true about the value in any of them. What crosses
// is the address of whoever is calling, and that is what it is now called.
const Header = "X-Client-Ip"
