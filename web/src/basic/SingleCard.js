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

import React from "react";
import * as Setting from "../Setting";
import {withRouter} from "react-router-dom";
import i18next from "i18next";

class SingleCard extends React.Component {
  constructor(props) {
    super(props);
    this.state = {
      classes: props,
    };
  }

  wrappedAsSilentSigninLink(link) {
    return link;
  }

  render() {
    const {logo, link, title, desc, time, isSingle} = this.props;
    const silentSigninLink = this.wrappedAsSilentSigninLink(link);

    return (
      <div
        className={`bg-zinc-900/50 border border-zinc-800 rounded-lg overflow-hidden cursor-pointer hover:border-zinc-700 transition-colors ${isSingle ? "max-w-[320px]" : ""}`}
        onClick={() => Setting.goToLinkSoft(this, silentSigninLink)}
      >
        <img alt="logo" src={logo} className="w-full h-[200px] object-scale-down p-4" />
        <div className="p-4 border-t border-zinc-800">
          <h3 className="text-sm font-medium text-white mb-1">{title}</h3>
          <p className="text-xs text-zinc-500 mb-2">{desc}</p>
          <p className="text-xs text-zinc-500">{i18next.t("message:Comment")}</p>
          <p className="text-xs text-zinc-500">{time}</p>
        </div>
      </div>
    );
  }
}

export default withRouter(SingleCard);
