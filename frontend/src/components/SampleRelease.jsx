import React from "react"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { NumberField, SettingBlock, SettingGroup, SettingRow } from "@/components/ui/setting"
import { ConfidenceChip } from "@/components/ConfidenceChip"
import { ChevronDown } from "lucide-react"
import { DEFAULT_SEADEX, EMPTY_SAMPLE, sampleActiveCount } from "@/lib/sample"
import { cn, selectClass } from "@/lib/utils"

const DEFAULT_PROBE = {
  height: 2160,
  video_codec: "hevc",
  audio_codec: "eac3",
  bit_depth: 10,
  hdr: "HDR10",
  dolby_vision: false,
  // Track languages are typed as comma-separated tags and normalized the way
  // a real probe's are (jpn → ja); sampleForRequest splits them.
  audio_languages: "",
  subtitle_languages: "",
  // Counts include untagged tracks, which the language lists cannot show.
  audio_streams: 0,
  subtitle_streams: 0,
}

const HDR_OPTIONS = [
  { value: "", label: "SDR" },
  { value: "HDR10", label: "HDR10" },
  { value: "HDR10+", label: "HDR10+" },
  { value: "HLG", label: "HLG" },
]

const AVAIL_OPTIONS = [
  { value: "unknown", label: "Nobody has reported it" },
  { value: "available", label: "Reported available" },
  { value: "unavailable", label: "Reported unavailable" },
]

// SampleRelease supplies the parts of a release a name cannot carry.
//
// Without it, a rule about size, grabs or a probed file is untestable: the
// preview builds its releases from the names you paste, so those attributes
// have no value and the rule is reported as unjudgeable. Filling them in here
// is what turns "I cannot check this" into "here is what it does".
//
// Each group is opt-in and off by default, because pretending by default would
// be worse than not pretending at all — a grabs rule that quietly judged every
// sample against an invented number is exactly the trap this replaces.
export function SampleRelease({ value, onChange, open, onOpenChange }) {
  const sample = value || EMPTY_SAMPLE
  const patch = (next) => onChange({ ...sample, ...next })
  const active = sampleActiveCount(sample)

  return (
    <div className="overflow-hidden rounded-lg border border-border/60 bg-card/40">
      <button
        type="button"
        onClick={() => onOpenChange(!open)}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left"
      >
        <ChevronDown className={cn("h-3.5 w-3.5 text-muted-foreground transition-transform", !open && "-rotate-90")} />
        <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Pretend the release also has
        </span>
        <span className="text-[11px] text-muted-foreground/70">
          {active > 0
            ? `${active} of 4 groups on`
            : "nothing — rules about size, grabs, the file, availability or SeaDex cannot be judged"}
        </span>
      </button>

      {open && (
        <div className="space-y-4 border-t border-border/50 p-3">
          <SettingGroup title="From the indexer" badge={<ConfidenceChip tier="reported" />}>
            <SettingRow
              label="Judge size, age and grabs"
              description="A release name carries none of these. Turn this on to answer rules that read them; leave it off and those rules are reported as unjudgeable instead of measured against nothing."
            >
              <Switch
                checked={sample.indexer_data === true}
                onCheckedChange={(v) => patch({ indexer_data: v })}
              />
            </SettingRow>

            {sample.indexer_data && (
              <>
                <SettingRow label="Size" description="Decimal GB, for the whole release.">
                  <NumberField value={sample.size_gb} onCommit={(size_gb) => patch({ size_gb })} min={0} step={0.5} />
                </SettingRow>
                <SettingRow label="Age" description="Days since it was posted.">
                  <NumberField value={sample.age_days} onCommit={(age_days) => patch({ age_days })} min={0} step={1} />
                </SettingRow>
                <SettingRow label="Grabs" description="How many people have fetched it.">
                  <NumberField value={sample.grabs} onCommit={(grabs) => patch({ grabs })} min={0} step={10} />
                </SettingRow>
                <SettingRow label="Indexer" description="The name a rule would match on.">
                  <Input
                    value={sample.indexer || ""}
                    onChange={(e) => patch({ indexer: e.target.value })}
                    placeholder="nzbgeek"
                    className="h-9 w-40 text-xs"
                  />
                </SettingRow>
                <SettingRow label="Password protected">
                  <Switch checked={sample.passworded === true} onCheckedChange={(v) => patch({ passworded: v })} />
                </SettingRow>
                <SettingRow label="Already in the library">
                  <Switch checked={sample.library === true} onCheckedChange={(v) => patch({ library: v })} />
                </SettingRow>
              </>
            )}
          </SettingGroup>

          <SettingGroup title="From ffprobe" badge={<ConfidenceChip tier="measured" />}>
            <SettingRow
              label="Pretend the file was probed"
              description="Only library releases are ever opened, so probe rules skip everything else. This stands in for one that was."
            >
              <Switch
                checked={Boolean(sample.probed)}
                onCheckedChange={(v) => patch({ probed: v ? { ...DEFAULT_PROBE } : null })}
              />
            </SettingRow>

            {sample.probed && (
              <>
                <SettingRow label="Height" description="Pixels, as measured.">
                  <NumberField
                    value={sample.probed.height}
                    onCommit={(height) => patch({ probed: { ...sample.probed, height } })}
                    min={0}
                    step={90}
                  />
                </SettingRow>
                <SettingRow label="Video codec">
                  <Input
                    value={sample.probed.video_codec || ""}
                    onChange={(e) => patch({ probed: { ...sample.probed, video_codec: e.target.value } })}
                    placeholder="hevc"
                    className="h-9 w-40 font-mono text-xs"
                  />
                </SettingRow>
                <SettingRow label="Bit depth">
                  <NumberField
                    value={sample.probed.bit_depth}
                    onCommit={(bit_depth) => patch({ probed: { ...sample.probed, bit_depth } })}
                    min={0}
                    step={2}
                  />
                </SettingRow>
                <SettingRow
                  label="HDR base layer"
                  htmlFor="sample-hdr"
                  description="What a device without Dolby Vision falls back to. SDR here plus Dolby Vision on is the DV-only case."
                >
                  <select
                    id="sample-hdr"
                    className={cn(selectClass, "w-40")}
                    value={sample.probed.hdr || ""}
                    onChange={(e) => patch({ probed: { ...sample.probed, hdr: e.target.value } })}
                  >
                    {HDR_OPTIONS.map((o) => <option key={o.label} value={o.value}>{o.label}</option>)}
                  </select>
                </SettingRow>
                <SettingRow
                  label="Audio track languages"
                  htmlFor="sample-audio-languages"
                  description="Tagged tracks only, comma-separated as a muxer writes them (jpn, eng, ara). Leave both lists and counts empty for a probe that read no tracks; rules on the tracks then skip."
                >
                  <Input
                    id="sample-audio-languages"
                    value={sample.probed.audio_languages || ""}
                    onChange={(e) => patch({ probed: { ...sample.probed, audio_languages: e.target.value } })}
                    placeholder="jpn, eng"
                    className="h-9 w-44 font-mono text-xs"
                  />
                </SettingRow>
                <SettingRow
                  label="Subtitle track languages"
                  htmlFor="sample-subtitle-languages"
                >
                  <Input
                    id="sample-subtitle-languages"
                    value={sample.probed.subtitle_languages || ""}
                    onChange={(e) => patch({ probed: { ...sample.probed, subtitle_languages: e.target.value } })}
                    placeholder="eng, ara"
                    className="h-9 w-44 font-mono text-xs"
                  />
                </SettingRow>
                <SettingRow
                  label="Audio tracks"
                  description="Total audio tracks, tagged or not. At least the number of languages above; higher stands in for untagged or duplicate-language tracks."
                >
                  <NumberField
                    value={sample.probed.audio_streams}
                    min={0}
                    onCommit={(audio_streams) => patch({ probed: { ...sample.probed, audio_streams } })}
                  />
                </SettingRow>
                <SettingRow
                  label="Subtitle tracks"
                  description="Total subtitle tracks, tagged or not."
                >
                  <NumberField
                    value={sample.probed.subtitle_streams}
                    min={0}
                    onCommit={(subtitle_streams) => patch({ probed: { ...sample.probed, subtitle_streams } })}
                  />
                </SettingRow>
                <SettingRow label="Dolby Vision">
                  <Switch
                    checked={sample.probed.dolby_vision === true}
                    onCheckedChange={(v) => patch({ probed: { ...sample.probed, dolby_vision: v } })}
                  />
                </SettingRow>
              </>
            )}
          </SettingGroup>

          <SettingGroup title="From AvailNZB" badge={<ConfidenceChip tier="community" />}>
            <SettingRow
              label="What the database says"
              htmlFor="sample-avail"
              description="Unknown is the common case in reality and leaves availability rules unjudged."
            >
              <select
                id="sample-avail"
                className={cn(selectClass, "w-52")}
                value={sample.avail_status || "unknown"}
                onChange={(e) => patch({ avail_status: e.target.value })}
              >
                {AVAIL_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            </SettingRow>

            {sample.avail_status && sample.avail_status !== "unknown" && (
              <>
                <SettingRow
                  label="On a backbone my providers use"
                  description="The stronger claim: alive somewhere you can actually reach."
                >
                  <Switch
                    checked={sample.avail_on_my_backbone === true}
                    onCheckedChange={(v) => patch({ avail_on_my_backbone: v })}
                  />
                </SettingRow>
                <SettingRow label="Checked" description="Days since the record was last updated.">
                  <NumberField
                    value={sample.avail_checked_days}
                    onCommit={(avail_checked_days) => patch({ avail_checked_days })}
                    min={0}
                    step={1}
                  />
                </SettingRow>
              </>
            )}
          </SettingGroup>

          <SettingGroup title="From SeaDex" badge={<ConfidenceChip tier="community" />}>
            <SettingRow
              label="Pretend a SeaDex lookup ran"
              description="Only anime requested through Kitsu is looked up, so SeaDex rules skip everything else. This stands in for a request that was."
            >
              <Switch
                checked={Boolean(sample.seadex)}
                onCheckedChange={(v) => patch({ seadex: v ? { ...DEFAULT_SEADEX } : null })}
              />
            </SettingRow>

            {sample.seadex && (
              <>
                <SettingRow
                  label="SeaDex knows the title"
                  description="Off simulates an anime SeaDex has not cataloged: rules still run, and seadex.known is no."
                >
                  <Switch
                    checked={sample.seadex.known === true}
                    onCheckedChange={(v) => patch({ seadex: { ...sample.seadex, known: v } })}
                  />
                </SettingRow>
                <SettingRow
                  label="Groups marked best"
                  description="Comma-separated release groups. A sampled release matches when its parsed group is one of these."
                >
                  <Input
                    value={sample.seadex.best_groups || ""}
                    onChange={(e) => patch({ seadex: { ...sample.seadex, best_groups: e.target.value } })}
                    placeholder="koala, SubsPlease"
                    className="h-9 w-52 font-mono text-xs"
                  />
                </SettingRow>
                <SettingRow
                  label="Recommended alternatives"
                  description="Groups SeaDex lists for the title without the best mark."
                >
                  <Input
                    value={sample.seadex.alt_groups || ""}
                    onChange={(e) => patch({ seadex: { ...sample.seadex, alt_groups: e.target.value } })}
                    placeholder="Commie"
                    className="h-9 w-52 font-mono text-xs"
                  />
                </SettingRow>
                <SettingRow
                  label="Groups with dual audio"
                  description="Groups whose recommended release SeaDex marks dual audio. A group may also be in either list above."
                >
                  <Input
                    value={sample.seadex.dual_audio_groups || ""}
                    onChange={(e) => patch({ seadex: { ...sample.seadex, dual_audio_groups: e.target.value } })}
                    placeholder="Anime Time"
                    className="h-9 w-52 font-mono text-xs"
                  />
                </SettingRow>
              </>
            )}
          </SettingGroup>

          <SettingBlock className="border-b-0 p-0">
            <p className="max-w-prose text-[11px] text-muted-foreground">
              These apply to every release name above, so a rule that depends on them is answered the same way for
              all of them. To compare a large release against a small one, change the size and run your eye down the
              list again.
            </p>
          </SettingBlock>
        </div>
      )}
    </div>
  )
}
