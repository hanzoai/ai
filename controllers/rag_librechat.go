// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package controllers

// The multipart file embed hanzo.chat's RAG client posts
// (api/server/services/Files/VectorDB/crud.js uploadVectors):
//
//	POST {base}/embed  multipart(file_id,file[,entity_id])
//	                   -> {status, known_type, file_id, filename}
//
// That request line is fixed by the client, not by us — a multipart form, not
// JSON — which is why it is the one address here that is not a spelling of
// /v1/ai/rag/embed. Everything the client READS it reads from the native
// surface (${origin}/v1/ai/rag/*). The handler is a thin HTTP projection over
// the ONE canonical RAG logic (object.Rag*); it holds no retrieval logic of its
// own, and controllers/rag.go shares that same logic.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/txt"
)

// isKnownType reports whether the filename's extension is a parser-supported
// type. hanzo.chat treats known_type=false as "filetype not supported".
func isKnownType(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return false
	}
	for _, t := range txt.GetSupportedFileTypes() {
		if t == ext {
			return true
		}
	}
	// Source-code / plaintext-ish files are parsed as plain text.
	return true
}

// RagEmbedMultipart handles POST /v1/embed — the multipart upload hanzo.chat's
// uploadVectors() sends. It saves the upload to a temp file, then reuses
// object.RagEmbedFile (same parse+chunk+index as the native surface).
//
// @Title RagEmbedMultipart
// @Tag RAG
// @Description Multipart file embed — the form-encoded twin of /v1/ai/rag/embed.
// @router /embed [post]
func (c *ApiController) RagEmbedMultipart() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	fileID := c.GetString("file_id")
	if fileID == "" {
		c.ragCompatError("file_id is required", "")
		return
	}
	entityID := c.GetString("entity_id") // reserved for shared-resource scoping

	f, header, err := c.GetFile("file")
	if err != nil {
		c.ragCompatError("file is required: "+err.Error(), fileID)
		return
	}
	defer f.Close()

	filename := header.Filename
	if !isKnownType(filename) {
		// Match the retired rag-api: report the unsupported type, don't 500.
		c.JSON(http.StatusOK, map[string]interface{}{
			"status": false, "known_type": false, "file_id": fileID, "filename": filename,
		})
		return
	}

	tmpPath, err := saveUploadToTemp(f, filename)
	if err != nil {
		c.ragCompatError("failed to buffer upload: "+err.Error(), fileID)
		return
	}
	defer os.Remove(tmpPath)

	// URL as a local path -> object.RagEmbedFile parses via txt.GetParsedTextFromUrl
	// (which treats a non-http url as a path), the SAME parser doc-ingest uses.
	req := &object.RagEmbedRequest{
		FileID:   fileID,
		Filename: filename,
		URL:      tmpPath,
		Tag:      entityID,
	}
	result, err := object.RagEmbedFile(auth.Owner, req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "index-docs", "rag-embed", "error", 0, c.Fiber().IP())
		c.ragCompatError(err.Error(), fileID)
		return
	}
	recordSearchUsage(auth, "index-docs", "rag-embed", "success", result.Chunks, c.Fiber().IP())

	c.JSON(http.StatusOK, map[string]interface{}{
		"status":     true,
		"known_type": true,
		"file_id":    fileID,
		"filename":   filename,
		"chunks":     result.Chunks,
	})
}

// ragCompatError emits the failure shape ({status:false,...}) hanzo.chat's
// uploadVectors() reads, so it surfaces a clean "File embedding failed".
func (c *ApiController) ragCompatError(msg, fileID string) {
	c.JSON(http.StatusOK, map[string]interface{}{
		"status": false, "known_type": true, "file_id": fileID, "detail": msg,
	})
}

// saveUploadToTemp streams an uploaded file to a temp path, preserving the
// extension so the parser can dispatch on it.
func saveUploadToTemp(f io.Reader, filename string) (string, error) {
	ext := filepath.Ext(filename)
	tmp, err := os.CreateTemp("", "rag-upload-*"+ext)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, f); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}
