// moveItem returns a copy of the list with the item moved from -> to. It is the
// state update every SortableList's onMove makes, so it lives once here rather
// than being re-inlined per consumer.
export function moveItem(list, from, to) {
  const next = [...list]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next
}

// uniquePreserveOrder drops duplicates and empty entries while keeping the
// order the user picked, which is what every ordered-selection list stores.
export function uniquePreserveOrder(values) {
  const seen = new Set()
  const next = []
  ;(Array.isArray(values) ? values : []).forEach((value) => {
    if (!value || seen.has(value)) return
    seen.add(value)
    next.push(value)
  })
  return next
}
