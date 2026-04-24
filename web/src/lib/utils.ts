import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatAge(dateStr: string | undefined): string {
  if (!dateStr) return "—";
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  if (diffSec < 60) return `${diffSec}s`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h`;
  const diffDay = Math.floor(diffHr / 24);
  return `${diffDay}d`;
}

export function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  return s.slice(0, max - 1) + "…";
}

export function phaseColor(phase: string | undefined): string {
  switch (phase?.toLowerCase()) {
    case "running":
      return "phase-running";
    case "succeeded":
    case "ready":
      return "phase-succeeded";
    case "failed":
    case "error":
      return "phase-failed";
    case "pending":
      return "phase-pending";
    case "downloading":
    case "loading":
      return "phase-running";
    case "serving":
      return "phase-serving";
    case "postrunning":
      return "phase-postrunning";
    default:
      return "bg-secondary text-muted-foreground";
  }
}
