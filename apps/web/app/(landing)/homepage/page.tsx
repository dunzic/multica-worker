import { MulticaLanding } from "@/features/landing/components/multica-landing";
import { FACT_HOME_METADATA } from "@/lib/fact-brand-metadata";

export const metadata = FACT_HOME_METADATA;

export default function HomepagePage() {
  return <MulticaLanding />;
}
