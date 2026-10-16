import { cn } from "@multica/ui/lib/utils";

/** Typographic interpretation of the brand note, not a replacement app icon. */
export function FactWordmark({
  companyName,
  className,
}: {
  companyName: string;
  className?: string;
}) {
  return (
    <span className={cn("fact-wordmark", className)}>
      <span className="fact-wordmark-name" translate="no">
        FACT<span aria-hidden="true">.</span>
      </span>
      <span className="fact-wordmark-company">{companyName}</span>
    </span>
  );
}
