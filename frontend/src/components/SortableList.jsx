import React from "react"
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core"
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable"
import { CSS } from "@dnd-kit/utilities"
import { GripVertical } from "lucide-react"
import { cn } from "@/lib/utils"

// SortableList is the shared vertical drag-reorder wrapper. It uses dnd-kit
// (pointer + keyboard sensors), so reordering works with a mouse, a finger,
// and the keyboard — the HTML5 drag events used elsewhere never fire on touch
// devices. The pointer sensor needs a small movement threshold so taps on the
// row's buttons don't start a drag.
export function SortableList({ ids, onMove, disabled, children }) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  )

  const handleDragEnd = ({ active, over }) => {
    if (!over || active.id === over.id) return
    const from = ids.indexOf(active.id)
    const to = ids.indexOf(over.id)
    if (from >= 0 && to >= 0) onMove(from, to)
  }

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
      <SortableContext items={ids} strategy={verticalListSortingStrategy} disabled={disabled}>
        {children}
      </SortableContext>
    </DndContext>
  )
}

// SortableRow renders one row with a grip handle (drag listeners live on the
// handle only, so the rest of the row scrolls and clicks normally on touch).
export function SortableRow({ id, disabled, className, children }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id, disabled })

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "flex items-center gap-2 rounded-md border border-border/60 px-2 py-2 sm:gap-3 sm:px-3",
        isDragging && "z-10 border-primary/40 bg-muted/50 shadow-lg",
        className
      )}
    >
      <button
        type="button"
        className={cn(
          "flex h-9 w-9 shrink-0 touch-none items-center justify-center rounded-md border border-dashed border-border/80 bg-muted/30 text-muted-foreground transition hover:bg-muted/50",
          disabled ? "cursor-default opacity-40" : "cursor-grab active:cursor-grabbing"
        )}
        aria-label="Reorder"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="h-4 w-4" />
      </button>
      {children}
    </div>
  )
}

