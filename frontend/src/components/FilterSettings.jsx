import React, { useMemo, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Switch } from "@/components/ui/switch"
import { CircleHelp, Plus, Trash2, X, ArrowUp, ArrowDown, GripVertical } from "lucide-react"

const RESOLUTION_OPTIONS = ["2160p", "1080p", "720p", "576p", "480p"]
const QUALITY_OPTIONS = ["CAM", "TeleSync", "TeleCine", "SCR", "WEB-DL", "WEBRip", "BluRay", "Remux", "BDRip", "BRRip", "HDTV", "DVD"]
const CODEC_OPTIONS = ["HEVC", "AVC", "MPEG-2", "x264", "x265"]
const HDR_OPTIONS = ["DV", "HDR10+", "HDR", "SDR"]

const DEFAULT_PROFILE = {
  name: "New Filter Profile",
  allowed_resolutions: [],
  blocked_resolutions: [],
  allowed_qualities: [],
  blocked_qualities: [],
  allowed_codecs: [],
  blocked_codecs: [],
  require_hdr: false,
  allowed_hdrs: [],
  blocked_hdrs: [],
  required_keywords: [],
  excluded_keywords: [],
  sort_order: [],
}

export function FilterSettings({ value = [], onChange, fieldErrors = {} }) {
  const [editingProfile, setEditingProfile] = useState(null)
  const [editingIndex, setEditingIndex] = useState(-1)
  const [showDialog, setShowDialog] = useState(false)
  const [newKeyword, setNewKeyword] = useState({ required: "", excluded: "" })

  const profiles = Array.isArray(value) ? value : []

  const getSortOrder = (profile) => {
    const defaults = ["resolution", "quality", "size", "age", "hdr", "codec", "grabs"]
    const current = profile?.sort_order || []
    const result = [...current]
    defaults.forEach(item => {
      if (!result.includes(item)) {
        result.push(item)
      }
    })
    return result
  }

  const moveSortItem = (index, direction) => {
    if (!editingProfile) return
    const current = getSortOrder(editingProfile)
    const targetIndex = index + direction
    if (targetIndex < 0 || targetIndex >= current.length) return
    const temp = current[index]
    current[index] = current[targetIndex]
    current[targetIndex] = temp
    setEditingProfile({ ...editingProfile, sort_order: current })
  }

  const handleSave = () => {
    if (!editingProfile || !editingProfile.name.trim()) return

    const nameLower = editingProfile.name.trim().toLowerCase()
    const isDuplicate = profiles.some((p, i) => i !== editingIndex && p.name.trim().toLowerCase() === nameLower)
    if (isDuplicate) {
      alert("A filter profile with this name already exists.")
      return
    }

    const updated = [...profiles]
    if (editingIndex >= 0) {
      updated[editingIndex] = editingProfile
    } else {
      updated.push(editingProfile)
    }

    onChange(updated)
    setShowDialog(false)
    setEditingProfile(null)
  }

  const handleDelete = (index) => {
    if (confirm("Are you sure you want to delete this filter profile?")) {
      const updated = profiles.filter((_, i) => i !== index)
      onChange(updated)
    }
  }

  const startEdit = (profile, index) => {
    setEditingProfile({
      ...DEFAULT_PROFILE,
      ...profile,
      allowed_resolutions: profile.allowed_resolutions || [],
      blocked_resolutions: profile.blocked_resolutions || [],
      allowed_qualities: profile.allowed_qualities || [],
      blocked_qualities: profile.blocked_qualities || [],
      allowed_codecs: profile.allowed_codecs || [],
      blocked_codecs: profile.blocked_codecs || [],
      allowed_hdrs: profile.allowed_hdrs || [],
      blocked_hdrs: profile.blocked_hdrs || [],
      required_keywords: profile.required_keywords || [],
      excluded_keywords: profile.excluded_keywords || [],
      sort_order: getSortOrder(profile),
    })
    setEditingIndex(index)
    setShowDialog(true)
  }

  const startAdd = () => {
    setEditingProfile({
      ...DEFAULT_PROFILE,
      name: `Filter Profile ${profiles.length + 1}`,
      sort_order: ["resolution", "quality", "size", "age", "hdr", "codec", "grabs"],
    })
    setEditingIndex(-1)
    setShowDialog(true)
  }

  const toggleArrayValue = (field, val) => {
    if (!editingProfile) return
    const current = editingProfile[field] || []
    const updated = current.includes(val) ? current.filter((v) => v !== val) : [...current, val]
    setEditingProfile({ ...editingProfile, [field]: updated })
  }

  const addKeyword = (field) => {
    const key = field === "required_keywords" ? "required" : "excluded"
    const otherField = field === "required_keywords" ? "excluded_keywords" : "required_keywords"
    const val = newKeyword[key].trim()
    if (!val || !editingProfile) return

    const otherList = editingProfile[otherField] || []
    if (otherList.map(v => v.toLowerCase()).includes(val.toLowerCase())) {
      alert(`The keyword "${val}" is already in the other keyword list.`);
      return
    }

    const current = editingProfile[field] || []
    if (!current.includes(val)) {
      setEditingProfile({ ...editingProfile, [field]: [...current, val] })
    }
    setNewKeyword({ ...newKeyword, [key]: "" })
  }

  const removeKeyword = (field, val) => {
    if (!editingProfile) return
    const current = editingProfile[field] || []
    setEditingProfile({ ...editingProfile, [field]: current.filter((v) => v !== val) })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-lg font-medium text-foreground">Filter Profiles</h2>
          <p className="text-sm text-muted-foreground">Define custom rules to filter candidates locally using Go-PTT metadata before generating playlists.</p>
        </div>
        <Button onClick={startAdd} size="sm">
          <Plus className="mr-2 h-4 w-4" /> Add Profile
        </Button>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        {profiles.map((profile, idx) => (
          <Card key={idx} className="relative overflow-hidden border border-border bg-card">
            <CardHeader className="pb-2">
              <div className="flex items-center justify-between">
                <CardTitle className="text-base font-semibold">{profile.name}</CardTitle>
                <div className="flex space-x-1">
                  <Button variant="ghost" size="sm" onClick={() => startEdit(profile, idx)}>Edit</Button>
                  <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => handleDelete(idx)}>
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </div>
              <CardDescription>
                {profile.require_hdr ? "Requires HDR • " : ""}
                {((profile.allowed_resolutions || []).length > 0 || (profile.blocked_resolutions || []).length > 0) ? "Resolutions configured • " : ""}
                {((profile.allowed_qualities || []).length > 0 || (profile.blocked_qualities || []).length > 0) ? "Qualities configured" : "No resolution/quality limits"}
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-2 text-xs text-muted-foreground">
              {profile.allowed_resolutions?.length > 0 && (
                <div>
                  <span className="font-medium text-foreground mr-1">Allowed Resolutions:</span>
                  {profile.allowed_resolutions.join(", ")}
                </div>
              )}
              {profile.blocked_qualities?.length > 0 && (
                <div>
                  <span className="font-medium text-foreground mr-1">Blocked Qualities:</span>
                  {profile.blocked_qualities.join(", ")}
                </div>
              )}
              {profile.excluded_keywords?.length > 0 && (
                <div>
                  <span className="font-medium text-foreground mr-1">Excluded Keywords:</span>
                  {profile.excluded_keywords.join(", ")}
                </div>
              )}
            </CardContent>
          </Card>
        ))}

        {profiles.length === 0 && (
          <div className="col-span-2 flex flex-col items-center justify-center rounded-lg border border-dashed border-border py-10 text-center">
            <CircleHelp className="h-8 w-8 text-muted-foreground mb-2" />
            <p className="text-sm font-medium text-foreground">No Filter Profiles configured</p>
            <p className="text-xs text-muted-foreground mt-1">Add a profile to start filtering release sizes/formats locally.</p>
          </div>
        )}
      </div>

      <Dialog open={showDialog} onOpenChange={setShowDialog}>
        <DialogContent className="max-w-2xl bg-card border border-border">
          <DialogHeader>
            <DialogTitle>{editingIndex >= 0 ? "Edit Filter Profile" : "Add Filter Profile"}</DialogTitle>
            <DialogDescription>Configure matching rules for resolutions, qualities, codecs, and title keywords.</DialogDescription>
          </DialogHeader>

          {editingProfile && (
            <div className="space-y-4 py-2 max-h-[70vh] overflow-y-auto pr-2">
              <div className="space-y-2">
                <Label htmlFor="profile-name">Profile Name</Label>
                <Input id="profile-name" value={editingProfile.name} onChange={(e) => setEditingProfile({ ...editingProfile, name: e.target.value })} />
              </div>

              {/* Resolutions Selection */}
              <div className="grid grid-cols-2 gap-4 border-t border-border pt-4">
                <div>
                  <Label className="mb-2 block">Allowed Resolutions</Label>
                  <div className="flex flex-wrap gap-1">
                    {RESOLUTION_OPTIONS.filter((res) => !editingProfile.blocked_resolutions?.includes(res)).map((res) => (
                      <Badge
                        key={res}
                        variant={editingProfile.allowed_resolutions?.includes(res) ? "default" : "outline"}
                        className="cursor-pointer"
                        onClick={() => toggleArrayValue("allowed_resolutions", res)}
                      >
                        {res}
                      </Badge>
                    ))}
                  </div>
                </div>
                <div>
                  <Label className="mb-2 block">Blocked Resolutions</Label>
                  <div className="flex flex-wrap gap-1">
                    {RESOLUTION_OPTIONS.filter((res) => !editingProfile.allowed_resolutions?.includes(res)).map((res) => (
                      <Badge
                        key={res}
                        variant={editingProfile.blocked_resolutions?.includes(res) ? "destructive" : "outline"}
                        className="cursor-pointer"
                        onClick={() => toggleArrayValue("blocked_resolutions", res)}
                      >
                        {res}
                      </Badge>
                    ))}
                  </div>
                </div>
              </div>

              {/* Qualities Selection */}
              <div className="grid grid-cols-2 gap-4 border-t border-border pt-4">
                <div>
                  <Label className="mb-2 block">Allowed Qualities</Label>
                  <div className="flex flex-wrap gap-1">
                    {QUALITY_OPTIONS.filter((q) => !editingProfile.blocked_qualities?.includes(q)).map((q) => (
                      <Badge
                        key={q}
                        variant={editingProfile.allowed_qualities?.includes(q) ? "default" : "outline"}
                        className="cursor-pointer"
                        onClick={() => toggleArrayValue("allowed_qualities", q)}
                      >
                        {q}
                      </Badge>
                    ))}
                  </div>
                </div>
                <div>
                  <Label className="mb-2 block">Blocked Qualities</Label>
                  <div className="flex flex-wrap gap-1">
                    {QUALITY_OPTIONS.filter((q) => !editingProfile.allowed_qualities?.includes(q)).map((q) => (
                      <Badge
                        key={q}
                        variant={editingProfile.blocked_qualities?.includes(q) ? "destructive" : "outline"}
                        className="cursor-pointer"
                        onClick={() => toggleArrayValue("blocked_qualities", q)}
                      >
                        {q}
                      </Badge>
                    ))}
                  </div>
                </div>
              </div>

              {/* Codecs & HDR */}
              <div className="grid grid-cols-2 gap-4 border-t border-border pt-4">
                <div>
                  <Label className="mb-2 block">Allowed Codecs</Label>
                  <div className="flex flex-wrap gap-1">
                    {CODEC_OPTIONS.map((c) => (
                      <Badge
                        key={c}
                        variant={editingProfile.allowed_codecs?.includes(c) ? "default" : "outline"}
                        className="cursor-pointer"
                        onClick={() => toggleArrayValue("allowed_codecs", c)}
                      >
                        {c}
                      </Badge>
                    ))}
                  </div>
                </div>
                <div className="space-y-4">
                  <div className="flex items-center space-x-2">
                    <Switch
                      id="require-hdr"
                      checked={editingProfile.require_hdr}
                      onCheckedChange={(checked) => setEditingProfile({ ...editingProfile, require_hdr: checked === true })}
                    />
                    <Label htmlFor="require-hdr">Require HDR</Label>
                  </div>
                </div>
              </div>

              {/* Stream Sorting & Ranking Priority */}
              <div className="border-t border-border pt-4 space-y-3">
                <div>
                  <Label className="text-base font-semibold">Stream Sorting & Ranking Priority</Label>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Arrange sorting criteria in order of preference. Releases are sorted by the first criterion, using subsequent ones as tie-breakers.
                  </p>
                </div>
                <div className="space-y-2 rounded-lg border border-border bg-muted/30 p-3">
                  {getSortOrder(editingProfile).map((item, index, arr) => {
                    const labelMap = {
                      resolution: { title: "Resolution", desc: "4K > 1080p > 720p > 480p" },
                      quality: { title: "Quality", desc: "Remux > BluRay > WEB-DL > HDTV" },
                      size: { title: "Release Size", desc: "Larger files rank higher" },
                      age: { title: "Release Age", desc: "Newer releases rank higher" },
                      hdr: { title: "HDR Status", desc: "Dolby Vision > HDR10+ > HDR > SDR" },
                      codec: { title: "Codec", desc: "HEVC > AVC > MPEG-2" },
                      grabs: { title: "Grabs count", desc: "More grabs rank higher" },
                    };
                    const info = labelMap[item] || { title: item, desc: "" };

                    return (
                      <div
                        key={item}
                        className="flex items-center justify-between rounded-md border border-border bg-card p-2 text-sm shadow-sm"
                      >
                        <div className="flex items-center space-x-3">
                          <GripVertical className="h-4 w-4 text-muted-foreground/60 cursor-grab" />
                          <span className="font-semibold text-xs text-foreground/55">#{index + 1}</span>
                          <div>
                            <span className="font-medium text-foreground">{info.title}</span>
                            <span className="text-xs text-muted-foreground ml-2">({info.desc})</span>
                          </div>
                        </div>
                        <div className="flex items-center space-x-1">
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="h-8 w-8 p-0"
                            disabled={index === 0}
                            onClick={() => moveSortItem(index, -1)}
                          >
                            <ArrowUp className="h-4 w-4" />
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="h-8 w-8 p-0"
                            disabled={index === arr.length - 1}
                            onClick={() => moveSortItem(index, 1)}
                          >
                            <ArrowDown className="h-4 w-4" />
                          </Button>
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>

              {/* Keywords filtering */}
              <div className="border-t border-border pt-4 grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>Required Keywords (AND)</Label>
                  <div className="flex space-x-2">
                    <Input
                      placeholder="e.g. MULTI"
                      value={newKeyword.required}
                      onChange={(e) => setNewKeyword({ ...newKeyword, required: e.target.value })}
                      onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addKeyword("required_keywords"))}
                    />
                    <Button type="button" size="sm" onClick={() => addKeyword("required_keywords")}>Add</Button>
                  </div>
                  <div className="flex flex-wrap gap-1 mt-2">
                    {editingProfile.required_keywords?.map((kw) => (
                      <Badge key={kw} variant="secondary" className="flex items-center space-x-1">
                        <span>{kw}</span>
                        <X className="h-3 w-3 cursor-pointer" onClick={() => removeKeyword("required_keywords", kw)} />
                      </Badge>
                    ))}
                  </div>
                </div>

                <div className="space-y-2">
                  <Label>Excluded Keywords (NOT)</Label>
                  <div className="flex space-x-2">
                    <Input
                      placeholder="e.g. subbed"
                      value={newKeyword.excluded}
                      onChange={(e) => setNewKeyword({ ...newKeyword, excluded: e.target.value })}
                      onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addKeyword("excluded_keywords"))}
                    />
                    <Button type="button" size="sm" onClick={() => addKeyword("excluded_keywords")}>Add</Button>
                  </div>
                  <div className="flex flex-wrap gap-1 mt-2">
                    {editingProfile.excluded_keywords?.map((kw) => (
                      <Badge key={kw} variant="destructive" className="flex items-center space-x-1">
                        <span>{kw}</span>
                        <X className="h-3 w-3 cursor-pointer" onClick={() => removeKeyword("excluded_keywords", kw)} />
                      </Badge>
                    ))}
                  </div>
                </div>
              </div>
            </div>
          )}

          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowDialog(false)}>Cancel</Button>
            <Button onClick={handleSave} disabled={!editingProfile?.name.trim()}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
