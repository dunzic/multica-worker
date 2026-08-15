import { LoginPage } from "@multica/views/auth";
import { DragStrip } from "@multica/views/platform";
import { useT } from "@multica/views/i18n";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { PrivateDeploymentTarget } from "../components/private-deployment-target";

function requireRuntimeAppUrl(): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  if (!runtimeConfig.ok) {
    throw new Error(
      "Invariant violated: DesktopLoginPage rendered before App accepted runtime config",
    );
  }
  return runtimeConfig.config.appUrl;
}

export function DesktopLoginPage() {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  const webUrl = requireRuntimeAppUrl();
  const { t } = useT("auth");

  return (
    <div className="flex h-screen flex-col">
      <DragStrip />
      <LoginPage
        logo={<MulticaIcon bordered size="lg" />}
        onSuccess={() => {
          // Auth store update triggers AppContent re-render → shows DesktopShell.
          // Initial workspace navigation happens in routes.tsx via IndexRedirect.
        }}
        extra={
          <PrivateDeploymentTarget
            currentApiUrl={runtimeConfig.ok ? runtimeConfig.config.apiUrl : undefined}
            currentAppUrl={webUrl}
            labels={{
              configure: t(($) => $.private_deployment.configure),
              title: t(($) => $.private_deployment.title),
              description: t(($) => $.private_deployment.description),
              apiUrl: t(($) => $.private_deployment.api_url),
              appUrl: t(($) => $.private_deployment.app_url),
              appUrlHint: t(($) => $.private_deployment.app_url_hint),
              current: t(($) => $.private_deployment.current),
              connect: t(($) => $.private_deployment.connect),
              connecting: t(($) => $.private_deployment.connecting),
              cancel: t(($) => $.private_deployment.cancel),
            }}
          />
        }
      />
    </div>
  );
}
