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

import React, {useState} from "react";
import {Globe} from "lucide-react";
import * as Setting from "./Setting";
import * as Conf from "./Conf";

function LanguageSelect({languages: propLanguages, style}) {
  const [open, setOpen] = useState(false);
  const langs = propLanguages ?? Setting.Countries.map(item => item.key);

  const filteredCountries = Setting.Countries.filter(c => langs.includes(c.key));

  if (filteredCountries.length === 0) {
    return null;
  }

  return (
    <div className="relative" style={style}>
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center justify-center w-10 h-10 rounded-md text-zinc-400 hover:text-white hover:bg-zinc-800 transition-colors"
      >
        <Globe className="w-5 h-5" />
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute right-0 top-full mt-1 w-44 py-1 bg-zinc-900 border border-zinc-800 rounded-lg shadow-xl z-50">
            {filteredCountries.map(country => (
              <button
                key={country.key}
                onClick={() => {
                  Setting.setLanguage(country.key);
                  setOpen(false);
                }}
                className="w-full flex items-center gap-2 px-3 py-2 text-sm text-zinc-300 hover:text-white hover:bg-zinc-800 transition-colors"
              >
                <img
                  className="w-5 h-4 rounded-sm object-cover"
                  alt={country.alt}
                  src={`${Conf.StaticBaseUrl}/flag-icons/${country.country}.svg`}
                />
                {country.label}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export default LanguageSelect;
