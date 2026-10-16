"use client";

import type { ReactNode } from "react";
import { ArrowDown, Files, Bot, UserRoundCheck } from "lucide-react";
import { FactWordmark } from "@multica/ui/components/common/fact-wordmark";
import { useT } from "../i18n";

export function useFactPrinciples() {
  const { t } = useT("auth");
  return [
    {
      letter: "F",
      name: "Fact-based",
      title: t(($) => $.brand.fact),
      description: t(($) => $.brand.fact_description),
    },
    {
      letter: "A",
      name: "Accountable",
      title: t(($) => $.brand.accountable),
      description: t(($) => $.brand.accountable_description),
    },
    {
      letter: "C",
      name: "Controllable",
      title: t(($) => $.brand.controllable),
      description: t(($) => $.brand.controllable_description),
    },
    {
      letter: "T",
      name: "Traceable",
      title: t(($) => $.brand.traceable),
      description: t(($) => $.brand.traceable_description),
    },
  ];
}

/** An explicitly illustrative workflow, never presented as live customer data. */
export function FactCollaborationVisual() {
  const { t } = useT("auth");
  const stages = [
    {
      Icon: Files,
      title: t(($) => $.brand.source),
      detail: t(($) => $.brand.source_detail),
    },
    {
      Icon: Bot,
      title: t(($) => $.brand.agent),
      detail: t(($) => $.brand.agent_detail),
    },
    {
      Icon: UserRoundCheck,
      title: t(($) => $.brand.review),
      detail: t(($) => $.brand.review_detail),
    },
  ];
  return (
    <figure className="fact-workflow">
      <figcaption className="fact-workflow-heading">
        <span>{t(($) => $.brand.collaboration)}</span>
        <span className="fact-workflow-caption">{t(($) => $.brand.illustration)}</span>
      </figcaption>
      <ol className="fact-workflow-stages">
        {stages.map(({ Icon, title, detail }, index) => (
          <li key={title}>
            <div className={`fact-stage fact-stage-${index}`}>
              <span className="fact-stage-icon">
                <Icon aria-hidden="true" />
              </span>
              <div>
                <strong>{title}</strong>
                <p>{detail}</p>
              </div>
              <span className="fact-stage-number" aria-hidden="true">
                {String(index + 1).padStart(2, "0")}
              </span>
            </div>
            {index < stages.length - 1 && (
              <ArrowDown className="fact-stage-arrow" aria-hidden="true" />
            )}
          </li>
        ))}
      </ol>
      <p className="fact-workflow-footnote">{t(($) => $.brand.human_judgment)}</p>
    </figure>
  );
}

/** Shared by web and desktop; authentication state remains in LoginPage. */
export function FactLoginFrame({
  children,
  logo,
}: {
  children: ReactNode;
  logo?: ReactNode;
}) {
  const { t } = useT("auth");
  const principles = useFactPrinciples();
  return (
    <div className="fact-brand fact-auth">
      <div className="fact-auth-layout">
        <aside className="fact-auth-story">
          {logo ?? <FactWordmark companyName={t(($) => $.brand.company)} />}
          <div className="fact-auth-message">
            <p className="fact-eyebrow">{t(($) => $.brand.promise)}</p>
            <p className="fact-auth-headline">
              {t(($) => $.brand.headline_first)}
              <br />
              <span>{t(($) => $.brand.headline_second)}</span>
            </p>
            <p className="fact-auth-mission">{t(($) => $.brand.mission)}</p>
            <div className="fact-auth-orbit" aria-hidden="true">
              <span>{t(($) => $.brand.name)}</span>
            </div>
          </div>
          <ul className="fact-auth-principles">
            {principles.map(({ letter, title }) => (
              <li key={letter}>
                <b translate="no">{letter}</b>
                <span>{title}</span>
              </li>
            ))}
          </ul>
        </aside>
        <main className="fact-auth-form">
          <div className="fact-auth-form-inner">
            <p className="fact-eyebrow">{t(($) => $.brand.product)}</p>
            {children}
            <p className="fact-auth-caption">{t(($) => $.brand.human_judgment)}</p>
          </div>
          <p className="fact-auth-company">{t(($) => $.brand.company_english)}</p>
        </main>
      </div>
    </div>
  );
}
