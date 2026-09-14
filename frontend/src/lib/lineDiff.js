// lineDiff compares two texts line by line and returns the hunks a diff view
// shows: each entry is a line with `kind` "same", "del" or "add", in order.
// It is the longest-common-subsequence diff, quadratic in line count, which
// is fine for the few hundred lines a template can reach and keeps the
// output minimal: only the lines that actually differ are marked.
export function lineDiff(before, after) {
  const a = before.split("\n")
  const b = after.split("\n")
  // lcs[i][j] is the length of the common subsequence of a[i:] and b[j:].
  const lcs = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1))
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  const out = []
  let i = 0
  let j = 0
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      out.push({ kind: "same", text: a[i] })
      i++
      j++
    } else if (i < a.length && (j >= b.length || lcs[i + 1][j] >= lcs[i][j + 1])) {
      // Ties go to the deletion so a replaced line reads "- old" then "+ new".
      out.push({ kind: "del", text: a[i] })
      i++
    } else {
      out.push({ kind: "add", text: b[j] })
      j++
    }
  }
  return out
}

// collapseUnchanged folds runs of unchanged lines longer than 2*context into
// the `context` lines either side of a change and one `{ kind: "skip", count }`
// marker for the rest, so a long template reads as the hunks that moved.
export function collapseUnchanged(lines, context = 2) {
  const out = []
  let run = []
  const flush = (atEnd) => {
    if (!run.length) return
    const keepHead = out.length ? context : 0
    const keepTail = atEnd ? 0 : context
    if (run.length <= keepHead + keepTail) {
      out.push(...run)
    } else {
      out.push(...run.slice(0, keepHead))
      out.push({ kind: "skip", count: run.length - keepHead - keepTail })
      if (keepTail) out.push(...run.slice(-keepTail))
    }
    run = []
  }
  for (const line of lines) {
    if (line.kind === "same") {
      run.push(line)
    } else {
      flush(false)
      out.push(line)
    }
  }
  flush(true)
  return out
}
