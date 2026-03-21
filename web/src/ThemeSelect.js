// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
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

import React, {useState} from "react";
import {Check, Minimize2, Moon, Sun} from "lucide-react";
import i18next from "i18next";

export const Themes = [
  {label: "Default", key: "default", Icon: Sun},
  {label: "Dark", key: "dark", Icon: Moon},
  {label: "Compact", key: "compact", Icon: Minimize2},
];

function ThemeSelect({themeAlgorithm = ["dark"], onChange}) {
  const [open, setOpen] = useState(false);

  const currentIcon = themeAlgorithm.includes("dark") ? Moon : Sun;
  const CurrentIcon = currentIcon;

  const handleClick = (key) => {
    let nextTheme;
    if (key === "compact") {
      if (themeAlgorithm.includes("compact")) {
        nextTheme = themeAlgorithm.filter(t => t !== "compact");
      } else {
        nextTheme = [...themeAlgorithm, "compact"];
      }
    } else {
      if (!themeAlgorithm.includes(key)) {
        if (key === "dark") {
          nextTheme = [...themeAlgorithm.filter(t => t !== "default"), key];
        } else {
          nextTheme = [...themeAlgorithm.filter(t => t !== "dark"), key];
        }
      } else {
        nextTheme = [...themeAlgorithm];
      }
    }
    if (onChange) {
      onChange(nextTheme);
    }
    setOpen(false);
  };

  return (
    <div className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center justify-center w-10 h-10 rounded-md text-zinc-400 hover:text-white hover:bg-zinc-800 transition-colors"
      >
        <CurrentIcon className="w-5 h-5" />
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute right-0 top-full mt-1 w-44 py-1 bg-zinc-900 border border-zinc-800 rounded-lg shadow-xl z-50">
            {Themes.map(({label, key, Icon}) => (
              <button
                key={key}
                onClick={() => handleClick(key)}
                className="w-full flex items-center gap-2 px-3 py-2 text-sm text-zinc-300 hover:text-white hover:bg-zinc-800 transition-colors"
              >
                <Icon className="w-4 h-4" />
                <span className="flex-1 text-left">{i18next.t(`theme:${label}`)}</span>
                {themeAlgorithm.includes(key) && <Check className="w-3 h-3 text-white" />}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export default ThemeSelect;
