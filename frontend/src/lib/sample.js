// EMPTY_SAMPLE is a release nobody has claimed anything about: no NZB behind
// it, never opened, never reported. It is what a pasted release name really
// is, so it is the honest default.
export const EMPTY_SAMPLE = {
  indexer_data: false,
  size_gb: 20,
  age_days: 30,
  grabs: 250,
  passworded: false,
  indexer: "",
  library: false,
  probed: null,
  avail_status: "unknown",
  avail_on_my_backbone: false,
  avail_checked_days: 7,
}


// activeCount says how much of the release is being pretended about, so the
// section header carries its own state while collapsed.
export function sampleActiveCount(sample) {
  let n = 0
  if (sample?.indexer_data) n += 1
  if (sample?.probed) n += 1
  if (sample?.avail_status && sample.avail_status !== "unknown") n += 1
  return n
}

