import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

// Presentation only: these guest-reported events are not authorization or proof
// of isolation. Keep the original output available for inspection.
export function CellnResult({ output }: { output: string }) {
  let answer: string | undefined;
  const tools: string[] = [];
  if (output.length <= 1_048_576) {
    for (const line of output.split("\n")) {
      if (!line.startsWith("CELLN_HARNESS_EVENT ")) continue;
      try {
        const event = JSON.parse(line.slice("CELLN_HARNESS_EVENT ".length));
        if (!event || typeof event !== "object") continue;
        if (event.type === "completed" && typeof event.answer === "string") answer = event.answer;
        if (event.type === "tool" && typeof event.name === "string") tools.push(event.name);
      } catch { /* Malformed output remains visible in the original trace. */ }
    }
  }
  if (answer === undefined) return <pre className="whitespace-pre-wrap break-words text-sm">{output}</pre>;
  return <div className="space-y-4" data-testid="celln-result">
    <div className="prose prose-sm prose-invert max-w-none break-words" data-testid="celln-answer">
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{answer}</ReactMarkdown>
    </div>
    {!!tools.length && <p className="text-xs text-muted-foreground">Harness-reported tool calls: {tools.join(" → ")}</p>}
    <details>
      <summary className="cursor-pointer text-sm text-muted-foreground">Raw Harness output</summary>
      <pre className="mt-3 whitespace-pre-wrap break-all text-xs">{output}</pre>
    </details>
  </div>;
}
