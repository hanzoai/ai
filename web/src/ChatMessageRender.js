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

import {marked} from "marked";
import DOMPurify from "dompurify";
import katex from "katex";
import "katex/dist/katex.min.css";
import hljs from "highlight.js";
import "highlight.js/styles/atom-one-dark-reasonable.css";

marked.setOptions({
  renderer: new marked.Renderer(),
  gfm: true,
  tables: true,
  breaks: true,
  pedantic: false,
  sanitize: false,
  smartLists: true,
  smartypants: true,
});

export function renderMarkdown(text) {
  const rawHtml = marked(text);
  let cleanHtml = DOMPurify.sanitize(rawHtml);
  cleanHtml = cleanHtml.replace(/<p>/g, "<div>").replace(/<\/p>/g, "</div>");
  cleanHtml = cleanHtml.replace(/<h1>/g, "<h2>").replace(/<(h[1-6])>/g, "<$1 style='margin-top: 20px; margin-bottom: 20px'>");
  cleanHtml = cleanHtml.replace(/<(ul)>/g, "<ul style='display: flex; flex-direction: column; gap: 10px; margin-top: 10px; margin-bottom: 10px'>").replace(/<(ol)>/g, "<ol style='display: flex; flex-direction: column; gap: 0px; margin-top: 20px; margin-bottom: 20px'>");
  cleanHtml = cleanHtml.replace(/<pre>/g, "<pre style='white-space: pre-wrap; white-space: -moz-pre-wrap; white-space: -pre-wrap; white-space: -o-pre-wrap; word-wrap: break-word;'>");
  return cleanHtml;
}

export function renderLatex(text) {
  const codeBlocks = [];
  const codeBlockPlaceholder = "___CODE_BLOCK___";
  text = text.replace(/```[\s\S]*?```/g, (match) => {
    codeBlocks.push(match);
    return codeBlockPlaceholder + (codeBlocks.length - 1) + codeBlockPlaceholder;
  });

  const displayDollarRegex = /\$\$([\s\S]+?)\$\$/g;
  const displayBracketRegex = /\\\[([\s\S]+?)\\\]/g;
  const inlineDollarRegex = /\$([^$\n]+?)\$/g;
  const inlineParenRegex = /\\\((.+?)\\\)/g;

  text = text.replace(displayDollarRegex, (match, formula) => {
    try {
      return katex.renderToString(formula, {throwOnError: false, displayMode: true});
    } catch (error) {
      return match;
    }
  });

  text = text.replace(displayBracketRegex, (match, formula) => {
    try {
      return katex.renderToString(formula, {throwOnError: false, displayMode: true});
    } catch (error) {
      return match;
    }
  });

  text = text.replace(inlineDollarRegex, (match, formula) => {
    try {
      return katex.renderToString(formula, {throwOnError: false, displayMode: false});
    } catch (error) {
      return match;
    }
  });

  text = text.replace(inlineParenRegex, (match, formula) => {
    try {
      return katex.renderToString(formula, {throwOnError: false, displayMode: false});
    } catch (error) {
      return match;
    }
  });

  text = text.replace(new RegExp(codeBlockPlaceholder + "(\\d+)" + codeBlockPlaceholder, "g"), (match, index) => {
    return codeBlocks[parseInt(index)];
  });

  return text;
}

export function renderCode(text) {
  const tempDiv = document.createElement("div");
  tempDiv.innerHTML = text;
  tempDiv.querySelectorAll("pre code").forEach((block) => {
    hljs.highlightBlock(block);
  });
  return tempDiv.innerHTML;
}

export function renderText(text) {
  let html;
  html = renderLatex(text);
  html = renderMarkdown(html);
  html = renderCode(html);
  return (
    <div dangerouslySetInnerHTML={{__html: html}} className="flex flex-col gap-0" />
  );
}

export function renderReason(text) {
  if (!text) {return null;}

  let html = renderLatex(text);
  html = renderMarkdown(html);
  html = renderCode(html);

  return (
    <div
      dangerouslySetInnerHTML={{__html: html}}
      className="flex flex-col gap-0 bg-secondary rounded italic text-muted-foreground"
    />
  );
}
