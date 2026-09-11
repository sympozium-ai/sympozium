import { PLANES, planeDescription, planeTitle, type CreationKind, type ExecutionPlane } from "@/lib/creation";
import { cn } from "@/lib/utils";

interface PlanePickerProps {
  value: ExecutionPlane;
  onChange: (plane: ExecutionPlane) => void;
  /** Differentiates the "Celln cell" (Run) vs "Celln parent" (Harness) copy. */
  kind: CreationKind;
  /** Planes that cannot be used yet (e.g. Celln without a native runtime). */
  disabledPlanes?: ExecutionPlane[];
  /** Reason shown on a disabled plane tile. */
  disabledHint?: Partial<Record<ExecutionPlane, string>>;
  className?: string;
}

/**
 * The single Kubernetes/Celln selector used by both the Run dialog and the
 * Agent/Harness wizard, so the choice looks and behaves identically everywhere.
 * Unavailable planes are disabled up front instead of failing after selection.
 */
export function PlanePicker({
  value,
  onChange,
  kind,
  disabledPlanes = [],
  disabledHint,
  className,
}: PlanePickerProps) {
  return (
    <div className={cn("grid gap-2 sm:grid-cols-2", className)}>
      {PLANES.map((plane) => {
        const active = value === plane.value;
        const disabled = disabledPlanes.includes(plane.value);
        const hint = disabled ? disabledHint?.[plane.value] : undefined;
        return (
          <button
            key={plane.value}
            type="button"
            disabled={disabled}
            onClick={() => onChange(plane.value)}
            aria-disabled={disabled}
            className={cn(
              "rounded-md border p-3 text-left transition-colors",
              disabled
                ? "cursor-not-allowed border-border/60 opacity-60"
                : active
                  ? "border-primary bg-primary/5"
                  : "border-border hover:bg-muted/40",
            )}
          >
            <p className="text-sm font-medium">{planeTitle(plane.value, kind)}</p>
            <p className="text-xs text-muted-foreground">{hint || planeDescription(plane.value, kind)}</p>
          </button>
        );
      })}
    </div>
  );
}
