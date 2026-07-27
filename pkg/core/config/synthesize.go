// Carrying pre-jhin filter profiles onto jhin's rank.Profile. Used once, when
// a config written before the migration is loaded; the stored profile is
// authoritative from then on.
package config

import (
	"regexp"
	"strings"

	"github.com/dreulavelle/jhin/rank"
)

// qualityAttrs maps the quality vocabulary the old filter profiles stored onto
// jhin attributes. Names not listed here have no jhin equivalent and are
// dropped during synthesis rather than silently mistranslated.
var qualityAttrs = map[string]rank.Attr{
	"web":          rank.AttrWeb,
	"web-dl":       rank.AttrWebDL,
	"webdl":        rank.AttrWebDL,
	"web-dlrip":    rank.AttrWebDLRip,
	"webrip":       rank.AttrWebRip,
	"webmux":       rank.AttrWebMux,
	"bluray":       rank.AttrBluRay,
	"blu-ray":      rank.AttrBluRay,
	"remux":        rank.AttrRemux,
	"bluray remux": rank.AttrRemux,
	"brrip":        rank.AttrBRRip,
	"bdrip":        rank.AttrBDRip,
	"uhdrip":       rank.AttrUHDRip,
	"hdrip":        rank.AttrHDRip,
	"dvd":          rank.AttrDVD,
	"dvdrip":       rank.AttrDVDRip,
	"hdtv":         rank.AttrHDTV,
	"hdtvrip":      rank.AttrHDTV,
	"pdtv":         rank.AttrPDTV,
	"tvrip":        rank.AttrTVRip,
	"satrip":       rank.AttrSATRip,
	"ppvrip":       rank.AttrPPVRip,
	"vhs":          rank.AttrVHS,
	"vhsrip":       rank.AttrVHSRip,
	"cam":          rank.AttrCam,
	"telesync":     rank.AttrTeleSync,
	"telecine":     rank.AttrTeleCine,
	"scr":          rank.AttrScreener,
	"screener":     rank.AttrScreener,
	"r5":           rank.AttrR5,
	"xvid":         rank.AttrXvid,
	"divx":         rank.AttrXvid,
}

var codecAttrs = map[string]rank.Attr{
	"avc":    rank.AttrAVC,
	"h264":   rank.AttrAVC,
	"h.264":  rank.AttrAVC,
	"x264":   rank.AttrAVC,
	"hevc":   rank.AttrHEVC,
	"h265":   rank.AttrHEVC,
	"h.265":  rank.AttrHEVC,
	"x265":   rank.AttrHEVC,
	"av1":    rank.AttrAV1,
	"xvid":   rank.AttrXvid,
	"divx":   rank.AttrXvid,
	"mpeg-2": rank.AttrMPEG,
	"mpeg2":  rank.AttrMPEG,
	"mpeg":   rank.AttrMPEG,
}

var hdrAttrs = map[string]rank.Attr{
	"dv":           rank.AttrDolbyVision,
	"dolby vision": rank.AttrDolbyVision,
	"hdr10+":       rank.AttrHDR10Plus,
	"hdr10plus":    rank.AttrHDR10Plus,
	"hdr":          rank.AttrHDR,
	"sdr":          rank.AttrSDR,
}

var resolutionValues = map[string]rank.Resolution{
	"2160p": rank.Res2160p,
	"4k":    rank.Res2160p,
	"uhd":   rank.Res2160p,
	"1440p": rank.Res1440p,
	"2k":    rank.Res1440p,
	"1080p": rank.Res1080p,
	"720p":  rank.Res720p,
	"576p":  rank.Res576p,
	"480p":  rank.Res480p,
	"360p":  rank.Res360p,
	"240p":  rank.Res240p,
	"sd":    rank.Res480p,
}

// hdrRequirePattern gates on any non-SDR tag, which is how the legacy
// RequireHDR flag behaved.
const hdrRequirePattern = `\b(DV|Dolby.?Vision|HDR10\+|HDR10|HDR)\b`

func normKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Synthesize builds a jhin profile from a pre-jhin filter profile. It is used
// once, when a config predating the migration is loaded; afterwards the stored
// rank.Profile is authoritative and the legacy fields are ignored.
func Synthesize(fp FilterProfileConfig) rank.Profile {
	p := rank.Default()
	p.Name = fp.Name

	// The legacy schema had no notion of trash removal, and enabling jhin's
	// veto here would reject releases the user used to get. Adult content is
	// the exception: it is blocked everywhere, migrated profiles included.
	p.Options.RemoveTrash = false
	p.Options.RemoveAdult = true

	applyResolutions(&p, fp)
	applyAttributes(&p, fp)
	applyPatterns(&p, fp)
	applyLanguages(&p, fp)

	return p
}

func applyResolutions(p *rank.Profile, fp FilterProfileConfig) {
	if len(fp.AllowedResolutions) > 0 {
		// An allow-list is exhaustive: anything unlisted is off.
		for res := range p.Resolutions {
			p.Resolutions[res] = false
		}
		p.Resolutions[rank.ResUnknown] = true
		for _, raw := range fp.AllowedResolutions {
			if res, ok := resolutionValues[normKey(raw)]; ok {
				p.Resolutions[res] = true
			}
		}
	}
	for _, raw := range fp.BlockedResolutions {
		if res, ok := resolutionValues[normKey(raw)]; ok {
			p.Resolutions[res] = false
		}
	}
}

func applyAttributes(p *rank.Profile, fp FilterProfileConfig) {
	if p.Attributes == nil {
		p.Attributes = map[rank.Attr]rank.Policy{}
	}

	// Allow-lists disable every sibling in the same family, so that listing
	// only BluRay rejects WEB-DL rather than merely preferring BluRay.
	denyUnlisted(p, fp.AllowedQualities, qualityAttrs)
	denyUnlisted(p, fp.AllowedCodecs, codecAttrs)
	denyUnlisted(p, fp.AllowedHDRs, hdrAttrs)

	denyListed(p, fp.BlockedQualities, qualityAttrs)
	denyListed(p, fp.BlockedCodecs, codecAttrs)
	denyListed(p, fp.BlockedHDRs, hdrAttrs)
}

func denyUnlisted(p *rank.Profile, allowed []string, family map[string]rank.Attr) {
	if len(allowed) == 0 {
		return
	}
	keep := map[rank.Attr]bool{}
	for _, raw := range allowed {
		if attr, ok := family[normKey(raw)]; ok {
			keep[attr] = true
		}
	}
	if len(keep) == 0 {
		return
	}
	for _, attr := range family {
		if keep[attr] {
			continue
		}
		p.Attributes[attr] = rank.Policy{Fetch: false, Rank: policyRank(p, attr)}
	}
}

func denyListed(p *rank.Profile, blocked []string, family map[string]rank.Attr) {
	for _, raw := range blocked {
		if attr, ok := family[normKey(raw)]; ok {
			p.Attributes[attr] = rank.Policy{Fetch: false, Rank: policyRank(p, attr)}
		}
	}
}

// policyRank keeps whatever score the attribute already carried, so blocking
// something changes only whether it is fetchable.
func policyRank(p *rank.Profile, attr rank.Attr) int {
	if pol, ok := p.Attributes[attr]; ok {
		return pol.Rank
	}
	if pol, ok := rank.DefaultPolicies[attr]; ok {
		return pol.Rank
	}
	return 0
}

func applyPatterns(p *rank.Profile, fp FilterProfileConfig) {
	// Require is conjunctive in jhin, matching the legacy behaviour where
	// every required keyword had to be present.
	for _, kw := range fp.RequiredKeywords {
		if kw = strings.TrimSpace(kw); kw != "" {
			p.Require = append(p.Require, regexp.QuoteMeta(kw))
		}
	}
	for _, kw := range fp.ExcludedKeywords {
		if kw = strings.TrimSpace(kw); kw != "" {
			p.Exclude = append(p.Exclude, regexp.QuoteMeta(kw))
		}
	}
	if fp.RequireHDR != nil && *fp.RequireHDR {
		p.Require = append(p.Require, hdrRequirePattern)
	}
}

func applyLanguages(p *rank.Profile, fp FilterProfileConfig) {
	p.Languages.Required = append(p.Languages.Required, normalizeCodes(fp.AllowedLanguages)...)
	p.Languages.Exclude = append(p.Languages.Exclude, normalizeCodes(fp.BlockedLanguages)...)
	p.Languages.Preferred = append(p.Languages.Preferred, normalizeCodes(fp.PreferredLanguages)...)

	// AllowEnglish would let English releases bypass a deliberate language
	// requirement, which the legacy schema never did.
	if len(p.Languages.Required) > 0 {
		p.Options.AllowEnglish = false
	}
}

func normalizeCodes(codes []string) []string {
	if len(codes) == 0 {
		return nil
	}
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if c = normKey(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}
