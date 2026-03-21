// Copyright 2023 Hanzo AI Inc. All Rights Reserved.
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

import React, {useEffect, useState} from "react";
import {Redirect} from "react-router-dom";
import * as StoreBackend from "./backend/StoreBackend";
import * as Setting from "./Setting";
import ChatPage from "./ChatPage";
import UsagePage from "./UsagePage";
import i18next from "i18next";

function HomePage({account}) {
  const [store, setStore] = useState(null);

  useEffect(() => {
    StoreBackend.getStore("admin", "_cloud_default_store_")
      .then((res) => {
        if (res.status === "ok") {
          if (res.data && typeof res.data2 === "string" && res.data2 !== "") {
            res.data.error = res.data2;
          }
          setStore(res.data);
        } else {
          Setting.showMessage("error", `${i18next.t("general:Failed to get")}: ${res.msg}`);
        }
      });
  }, []);

  if (Setting.isAnonymousUser(account) || Setting.isChatUser(account) || Setting.getUrlParam("isRaw") !== null) {
    if (!account) {
      return null;
    }
    return <ChatPage account={account} />;
  }

  if (account?.tag === "Video") {
    return <Redirect to="/videos" />;
  }

  if (store === null) {
    return null;
  }

  if (Setting.canViewAllUsers(account)) {
    return <UsagePage account={account} />;
  }

  return <ChatPage account={account} />;
}

export default HomePage;
