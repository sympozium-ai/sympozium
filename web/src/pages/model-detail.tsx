import { useParams, Link, useNavigate } from "react-router-dom";
import { useModel, useDeleteModel } from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ArrowLeft, Trash2, Copy, Check } from "lucide-react";
import { formatAge } from "@/lib/utils";
import { useState } from "react";

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between text-sm py-1">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono text-right">{value ?? "--"}</span>
    </div>
  );
}

function CopyableValue({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);

  function copy() {
    navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }

  return (
    <span className="flex items-center gap-1">
      <span className="truncate max-w-[400px]">{value}</span>
      <button
        onClick={copy}
        className="text-muted-foreground hover:text-foreground"
        title="Copy"
      >
        {copied ? (
          <Check className="h-3 w-3 text-green-500" />
        ) : (
          <Copy className="h-3 w-3" />
        )}
      </button>
    </span>
  );
}

export function ModelDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: model, isLoading } = useModel(name || "");
  const deleteModel = useDeleteModel();
  const navigate = useNavigate();

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!model) {
    return <p className="text-muted-foreground">Model not found</p>;
  }

  function handleDelete() {
    if (!name) return;
    deleteModel.mutate(name, {
      onSuccess: () => navigate("/models"),
    });
  }

  const modelRefYaml = `spec:
  model:
    modelRef: ${model.metadata.name}`;

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Link
            to="/models"
            className="text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="h-5 w-5" />
          </Link>
          <div>
            <h1 className="text-2xl font-bold font-mono">
              {model.metadata.name}
            </h1>
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              Created {formatAge(model.metadata.creationTimestamp)} ago
              <StatusBadge phase={model.status?.phase} />
            </p>
          </div>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={handleDelete}
          disabled={deleteModel.isPending}
          className="text-destructive hover:text-destructive"
        >
          <Trash2 className="mr-2 h-4 w-4" />
          Delete
        </Button>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        {/* Status */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Status</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            <Row label="Phase" value={<StatusBadge phase={model.status?.phase} />} />
            <Row label="Message" value={model.status?.message} />
            {model.status?.endpoint && (
              <Row
                label="Endpoint"
                value={<CopyableValue value={model.status.endpoint} />}
              />
            )}
          </CardContent>
        </Card>

        {/* Source */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Source</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            <Row
              label="URL"
              value={
                <span className="truncate max-w-[300px] block text-right">
                  {model.spec.source.url}
                </span>
              }
            />
            <Row label="Filename" value={model.spec.source.filename || "model.gguf"} />
            <Row label="Storage" value={model.spec.storage?.size || "10Gi"} />
          </CardContent>
        </Card>

        {/* Resources */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Resources</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            <Row label="GPU" value={model.spec.resources?.gpu ?? 1} />
            <Row label="Memory" value={model.spec.resources?.memory || "16Gi"} />
            <Row label="CPU" value={model.spec.resources?.cpu || "4"} />
          </CardContent>
        </Card>

        {/* Inference */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Inference Server</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1">
            <Row
              label="Image"
              value={model.spec.inference?.image || "ghcr.io/ggml-org/llama.cpp:server"}
            />
            <Row label="Port" value={model.spec.inference?.port || 8080} />
            <Row label="Context Size" value={model.spec.inference?.contextSize || 4096} />
            {model.spec.inference?.args && model.spec.inference.args.length > 0 && (
              <Row label="Args" value={model.spec.inference.args.join(" ")} />
            )}
          </CardContent>
        </Card>
      </div>

      {/* Usage snippet */}
      {model.status?.phase === "Ready" && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Usage</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xs text-muted-foreground mb-2">
              Reference this model in an AgentRun spec:
            </p>
            <pre className="bg-muted/50 rounded-md p-3 text-xs font-mono overflow-x-auto">
              {modelRefYaml}
            </pre>
          </CardContent>
        </Card>
      )}

      {/* Conditions */}
      {model.status?.conditions && model.status.conditions.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Conditions</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              {model.status.conditions.map((c) => (
                <div
                  key={c.type}
                  className="flex items-center justify-between text-sm border-b border-border/50 pb-2 last:border-0"
                >
                  <div>
                    <span className="font-medium">{c.type}</span>
                    <span className="text-muted-foreground ml-2">
                      {c.reason}
                    </span>
                  </div>
                  <StatusBadge
                    phase={c.status === "True" ? "Ready" : "Pending"}
                  />
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
