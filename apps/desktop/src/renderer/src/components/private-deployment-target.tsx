import { useState, type FormEvent } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  deriveAppUrl,
  runtimeConfigFromTarget,
} from "../../../shared/runtime-config";

export interface PrivateDeploymentTargetLabels {
  configure: string;
  title: string;
  description: string;
  apiUrl: string;
  appUrl: string;
  appUrlHint: string;
  current: string;
  connect: string;
  connecting: string;
  cancel: string;
}

const DEFAULT_LABELS: PrivateDeploymentTargetLabels = {
  configure: "Configure private deployment",
  title: "Private deployment target",
  description: "Connect this Desktop app to your organization's Multica server.",
  apiUrl: "API address",
  appUrl: "Web address (optional)",
  appUrlHint: "Leave empty when the API and web app use the same host.",
  current: "Current target",
  connect: "Save and restart",
  connecting: "Restarting...",
  cancel: "Cancel",
};

function explicitAppUrl(
  currentApiUrl: string | undefined,
  currentAppUrl: string | undefined,
): string {
  if (!currentApiUrl || !currentAppUrl) return "";
  try {
    return deriveAppUrl(currentApiUrl) === currentAppUrl ? "" : currentAppUrl;
  } catch {
    return "";
  }
}

export function PrivateDeploymentTarget({
  currentApiUrl,
  currentAppUrl,
  initialOpen = false,
  labels = DEFAULT_LABELS,
}: {
  currentApiUrl?: string;
  currentAppUrl?: string;
  initialOpen?: boolean;
  labels?: PrivateDeploymentTargetLabels;
}) {
  const [open, setOpen] = useState(initialOpen);
  const [apiUrl, setApiUrl] = useState(currentApiUrl ?? "");
  const [appUrl, setAppUrl] = useState(
    explicitAppUrl(currentApiUrl, currentAppUrl),
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    setError("");

    const target = {
      apiUrl: apiUrl.trim(),
      ...(appUrl.trim() ? { appUrl: appUrl.trim() } : {}),
    };
    try {
      runtimeConfigFromTarget(target);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return;
    }

    setSaving(true);
    try {
      const result = await window.desktopAPI.setRuntimeTarget(target);
      if (!result.ok) {
        setError(result.message);
        setSaving(false);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving(false);
    }
  };

  if (!open) {
    return (
      <div className="space-y-1">
        {currentApiUrl && (
          <p className="truncate text-caption text-muted-foreground">
            {labels.current}: {currentApiUrl}
          </p>
        )}
        <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(true)}>
          {labels.configure}
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={save} className="space-y-3 text-left">
      <div>
        <p className="text-body font-medium text-foreground">{labels.title}</p>
        <p className="mt-1 text-caption text-muted-foreground">{labels.description}</p>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="private-deployment-api-url">{labels.apiUrl}</Label>
        <Input
          id="private-deployment-api-url"
          type="url"
          inputMode="url"
          placeholder="https://multica.internal.example"
          value={apiUrl}
          onChange={(event) => setApiUrl(event.target.value)}
          disabled={saving}
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="private-deployment-app-url">{labels.appUrl}</Label>
        <Input
          id="private-deployment-app-url"
          type="url"
          inputMode="url"
          placeholder="https://app.internal.example"
          value={appUrl}
          onChange={(event) => setAppUrl(event.target.value)}
          disabled={saving}
        />
        <p className="text-caption text-muted-foreground">{labels.appUrlHint}</p>
      </div>
      {error && <p className="text-caption text-destructive">{error}</p>}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={saving || !apiUrl.trim()}>
          {saving ? labels.connecting : labels.connect}
        </Button>
        {!initialOpen && (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={saving}
            onClick={() => {
              setOpen(false);
              setError("");
            }}
          >
            {labels.cancel}
          </Button>
        )}
      </div>
    </form>
  );
}
