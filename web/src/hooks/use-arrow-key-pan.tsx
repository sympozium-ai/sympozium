import { useEffect, useRef } from "react";
import { useReactFlow } from "@xyflow/react";

/**
 * True when the event originated from a field the user is typing into, so the
 * canvas should leave the keystroke alone (e.g. typing "A" in the stimulus
 * dialog must insert a character, not zoom the viewport).
 */
function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return (
    tag === "INPUT" ||
    tag === "TEXTAREA" ||
    tag === "SELECT" ||
    target.isContentEditable
  );
}

/**
 * Smooth arrow-key panning + A/Z zoom for ReactFlow canvases.
 * Uses requestAnimationFrame for 60fps movement with momentum.
 */
export function useArrowKeyPan() {
  const { getViewport, setViewport } = useReactFlow();
  const keysDown = useRef(new Set<string>());
  const velocity = useRef({ x: 0, y: 0 });
  const rafId = useRef<number>(0);

  useEffect(() => {
    const PAN_ACCEL = 1.0;
    const MAX_SPEED = 16;
    const FRICTION = 0.90;
    const ZOOM_SPEED = 0.006;

    function tick() {
      const v = velocity.current;
      const held = keysDown.current;

      if (held.has("ArrowLeft"))  v.x += PAN_ACCEL;
      if (held.has("ArrowRight")) v.x -= PAN_ACCEL;
      if (held.has("ArrowUp"))    v.y += PAN_ACCEL;
      if (held.has("ArrowDown"))  v.y -= PAN_ACCEL;

      v.x = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, v.x));
      v.y = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, v.y));

      if (!held.has("ArrowLeft") && !held.has("ArrowRight")) v.x *= FRICTION;
      if (!held.has("ArrowUp") && !held.has("ArrowDown"))    v.y *= FRICTION;

      if (Math.abs(v.x) < 0.3) v.x = 0;
      if (Math.abs(v.y) < 0.3) v.y = 0;

      const vp = getViewport();
      let zoom = vp.zoom;
      let { x, y } = vp;

      const zoomDir = (held.has("a") ? 1 : 0) - (held.has("z") ? 1 : 0);
      if (zoomDir !== 0) {
        const newZoom = Math.max(0.1, Math.min(2, zoom + zoomDir * ZOOM_SPEED * zoom));
        const ratio = newZoom / zoom;
        const cx = window.innerWidth / 2;
        const cy = window.innerHeight / 2;
        x = cx - (cx - x) * ratio;
        y = cy - (cy - y) * ratio;
        zoom = newZoom;
      }

      if (v.x !== 0 || v.y !== 0 || zoom !== vp.zoom) {
        setViewport({ x: x + v.x, y: y + v.y, zoom });
      }

      rafId.current = requestAnimationFrame(tick);
    }

    const TRACKED = new Set(["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "a", "z"]);

    function onKeyDown(e: KeyboardEvent) {
      // Don't hijack keys while the user is typing in a form field/dialog.
      if (isEditableTarget(e.target)) return;
      const key = e.key.length === 1 ? e.key.toLowerCase() : e.key;
      if (!TRACKED.has(key)) return;
      e.preventDefault();
      e.stopPropagation();
      keysDown.current.add(key);
    }
    function onKeyUp(e: KeyboardEvent) {
      const key = e.key.length === 1 ? e.key.toLowerCase() : e.key;
      keysDown.current.delete(key);
    }

    rafId.current = requestAnimationFrame(tick);
    window.addEventListener("keydown", onKeyDown, true);
    window.addEventListener("keyup", onKeyUp, true);
    return () => {
      cancelAnimationFrame(rafId.current);
      window.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("keyup", onKeyUp, true);
    };
  }, [getViewport, setViewport]);
}

/** Keyboard guide overlay — render inside <ReactFlow>. */
export function KeyboardGuide() {
  return (
    <div className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10 flex items-center gap-4 px-4 py-2 bg-card/80 backdrop-blur border border-border/50 rounded text-[10px] text-muted-foreground select-none pointer-events-none">
      <span className="flex items-center gap-1.5">
        <kbd className="px-1.5 py-0.5 bg-muted rounded text-[9px] font-mono border border-border/50">&#8592;</kbd>
        <kbd className="px-1.5 py-0.5 bg-muted rounded text-[9px] font-mono border border-border/50">&#8593;</kbd>
        <kbd className="px-1.5 py-0.5 bg-muted rounded text-[9px] font-mono border border-border/50">&#8595;</kbd>
        <kbd className="px-1.5 py-0.5 bg-muted rounded text-[9px] font-mono border border-border/50">&#8594;</kbd>
        <span>Pan</span>
      </span>
      <span className="text-border/50">|</span>
      <span className="flex items-center gap-1.5">
        <kbd className="px-1.5 py-0.5 bg-muted rounded text-[9px] font-mono border border-border/50">A</kbd>
        <span>Zoom in</span>
      </span>
      <span className="flex items-center gap-1.5">
        <kbd className="px-1.5 py-0.5 bg-muted rounded text-[9px] font-mono border border-border/50">Z</kbd>
        <span>Zoom out</span>
      </span>
    </div>
  );
}
