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

package object

// ClientIPHeader carries the caller's address from a host in front to this module.
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
const ClientIPHeader = "X-Hanzo-Client-Ip"
