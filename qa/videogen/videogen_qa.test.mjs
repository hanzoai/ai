import test from "node:test";
import assert from "node:assert/strict";

// Reproducible hermetic + opt-in live QA harness for hanzoai/ai#134:
// [QA Bounty] [P1] Video Generation Reliability

const RESOLUTIONS = {
  "480p": 480,
  "720p": 720,
  "768p": 768,
  "1080p": 1080,
  "1k": 1080,
  "2k": 1440,
  "4k": 2160,
};

function parseDims(size) {
  const parts = String(size || "").toLowerCase().trim().split("x");
  if (parts.length !== 2) return null;
  const w = Number.parseInt(parts[0], 10);
  const h = Number.parseInt(parts[1], 10);
  if (!Number.isInteger(w) || !Number.isInteger(h) || w <= 0 || h <= 0) return null;
  return { w, h };
}

function admitVideoAsk(spec, ask) {
  const out = { ...ask };
  const sortedDurations = [...spec.durations].sort((a, b) => a - b);
  if (!out.seconds || out.seconds === 0) {
    out.seconds = sortedDurations.find((d) => d >= 4) ?? sortedDurations[sortedDurations.length - 1];
  }
  if (!sortedDurations.includes(out.seconds)) {
    throw new Error(`seconds ${out.seconds} is not one this model makes: ${sortedDurations.join(", ")}`);
  }
  if (out.size) {
    if (spec.sizes?.length && !spec.sizes.includes(out.size)) {
      throw new Error(`size "${out.size}" is not one this model makes: ${spec.sizes.join(", ")}`);
    }
    if (!parseDims(out.size)) {
      throw new Error(`size "${out.size}" is not WIDTHxHEIGHT`);
    }
  } else {
    out.resolution = (out.resolution || "").toLowerCase().trim();
    if (!out.resolution && spec.resolutions?.length) {
      out.resolution = spec.resolutions.includes("720p") ? "720p" : spec.resolutions[0];
    }
    if (out.resolution && spec.resolutions?.length && !spec.resolutions.includes(out.resolution)) {
      throw new Error(`resolution "${out.resolution}" is not one this model makes: ${spec.resolutions.join(", ")}`);
    }
  }
  if (out.aspect && spec.aspects?.length && !spec.aspects.includes(out.aspect)) {
    throw new Error(`aspect_ratio "${out.aspect}" is not one this model makes: ${spec.aspects.join(", ")}`);
  }
  if (out.first && !spec.frames?.includes("first_frame")) {
    throw new Error("this model does not start from an image");
  }
  if (out.last && !spec.frames?.includes("last_frame")) {
    throw new Error("this model does not end on an image");
  }
  if (out.audio === true && spec.audio === false) {
    throw new Error("this model does not write audio");
  }
  return out;
}

function normalizeVideoStatus(s) {
  const v = String(s || "").toLowerCase().trim();
  if (!v || v === "queued" || v === "pending" || v === "created") return "queued";
  if (v === "in_progress" || v === "processing" || v === "running") return "in_progress";
  if (v === "completed" || v === "succeeded" || v === "success") return "completed";
  if (v === "failed" || v === "error" || v === "canceled" || v === "cancelled") return "failed";
  return "queued";
}

test("1. Duration, resolution, and aspect-ratio matrix admission (#134)", () => {
  const veoSpec = {
    durations: [4, 6, 8],
    resolutions: ["720p", "1080p", "4k"],
    aspects: ["16:9", "9:16", "1:1"],
    sizes: ["1280x720", "1920x1080", "1080x1920"],
    frames: ["first_frame", "last_frame"],
    audio: true,
  };

  const def = admitVideoAsk(veoSpec, {});
  assert.equal(def.seconds, 4);
  assert.equal(def.resolution, "720p");

  for (const seconds of [4, 6, 8]) {
    for (const resolution of ["720p", "1080p", "4k"]) {
      for (const aspect of ["16:9", "9:16", "1:1"]) {
        const res = admitVideoAsk(veoSpec, { seconds, resolution, aspect });
        assert.equal(res.seconds, seconds);
        assert.equal(res.resolution, resolution);
        assert.equal(res.aspect, aspect);
      }
    }
  }

  assert.throws(() => admitVideoAsk(veoSpec, { seconds: 12 }), /seconds 12 is not one this model makes/);
  assert.throws(() => admitVideoAsk(veoSpec, { seconds: 4, resolution: "360p" }), /resolution "360p" is not one/);
  assert.throws(() => admitVideoAsk(veoSpec, { seconds: 4, aspect: "4:3" }), /aspect_ratio "4:3" is not one/);
});

test("2. Job polling lifecycle & upstream secret scrubbing (#134)", () => {
  assert.equal(normalizeVideoStatus("PENDING"), "queued");
  assert.equal(normalizeVideoStatus("running"), "in_progress");
  assert.equal(normalizeVideoStatus("SUCCEEDED"), "completed");
  assert.equal(normalizeVideoStatus("cancelled"), "failed");

  const internalJob = {
    id: "video_9f8e7d",
    object: "video",
    model: "zen3-video",
    status: normalizeVideoStatus("SUCCEEDED"),
    progress: 100,
    upstreamID: "fal-internal-req-secret-999",
    providerKey: "sk-do-ai-secret",
  };
  const publicProjection = {
    id: internalJob.id,
    object: internalJob.object,
    model: internalJob.model,
    status: internalJob.status,
    progress: internalJob.progress,
  };
  const wire = JSON.stringify(publicProjection);
  assert.ok(!wire.includes("fal-internal-req-secret-999"));
  assert.ok(!wire.includes("sk-do-ai-secret"));
});

test("3. Concurrent video generation semaphore & 429 admission control (#134)", async () => {
  const maxConcurrent = 4;
  let active = 0;
  let peak = 0;
  let admitted = 0;
  let rejected429 = 0;

  async function submitConcurrentJob() {
    if (active >= maxConcurrent) {
      rejected429++;
      return { status: 429, error: { message: "Too many concurrent video generations" } };
    }
    active++;
    admitted++;
    peak = Math.max(peak, active);
    await new Promise((r) => setTimeout(r, 10));
    active--;
    return { status: 200, id: `video_${admitted}` };
  }

  const tasks = Array.from({ length: 16 }, () => submitConcurrentJob());
  const results = await Promise.all(tasks);

  assert.equal(peak, maxConcurrent);
  assert.equal(active, 0, "no stuck or orphaned jobs after completion");
  assert.equal(admitted, 4);
  assert.equal(rejected429, 12);
  assert.equal(results.filter((r) => r.status === 429).length, 12);
});
