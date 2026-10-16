"use client";

import Link from "next/link";
import { ArrowRight, Download, Globe2 } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { FactWordmark } from "@multica/ui/components/common/fact-wordmark";
import { FactCollaborationVisual, useFactPrinciples } from "@multica/views/auth";
import { useT } from "@multica/views/i18n";
import { useLocale, localeLabels, type Locale } from "../i18n";
import { useDashboardCtaHref } from "../utils/use-dashboard-cta";
import "./fact-landing.css";

const brandLocales: Locale[] = ["zh-Hans", "en", "ja", "ko", "fr"];

export function MulticaLanding() {
  const { t } = useT("auth");
  const { locale, setLocale } = useLocale();
  const user = useAuthStore((state) => state.user);
  const ctaHref = useDashboardCtaHref();
  const ctaLabel = user ? t(($) => $.brand.enter) : t(($) => $.brand.login);
  const principles = useFactPrinciples();
  const fields = [t(($) => $.brand.forensics), t(($) => $.brand.audit), t(($) => $.brand.consulting), t(($) => $.brand.taxation)];

  return (
    <div className="fact-brand fact-landing">
      <a className="fact-skip-link" href="#fact-main">{t(($) => $.brand.skip)}</a>
      <header className="fact-header fact-container">
        <Link href="/homepage" aria-label={t(($) => $.brand.home)} className="fact-home-link">
          <FactWordmark companyName={t(($) => $.brand.company)} />
        </Link>
        <nav aria-label={t(($) => $.brand.navigation)} className="fact-nav">
          <Link href="#collaboration">{t(($) => $.brand.approach)}</Link>
          <Link href="#principles">{t(($) => $.brand.principles)}</Link>
        </nav>
        <div className="fact-header-actions">
          <label className="fact-language">
            <Globe2 size={16} aria-hidden="true" />
            <span className="sr-only">{t(($) => $.brand.language)}</span>
            <select value={locale} onChange={(event) => {
              const selected = brandLocales.find((item) => item === event.target.value);
              if (selected) setLocale(selected);
            }}>
              {brandLocales.map((item) => <option key={item} value={item}>{localeLabels[item]}</option>)}
            </select>
          </label>
          <Link href={ctaHref} className="fact-button fact-button-small">{ctaLabel}<ArrowRight aria-hidden="true" size={16} /></Link>
        </div>
      </header>

      <main id="fact-main" tabIndex={-1}>
        <section className="fact-hero fact-container" id="collaboration">
          <div className="fact-hero-copy">
            <p className="fact-eyebrow"><span className="fact-eyebrow-line" aria-hidden="true" />{t(($) => $.brand.promise)}</p>
            <h1>{t(($) => $.brand.headline_first)}<br /><span>{t(($) => $.brand.headline_second)}</span></h1>
            <p className="fact-hero-intro">{t(($) => $.brand.intro)}</p>
            <div className="fact-hero-actions">
              <Link href={ctaHref} className="fact-button">{ctaLabel}<ArrowRight size={18} aria-hidden="true" /></Link>
              <Link href="/download" className="fact-button fact-button-secondary"><Download size={18} aria-hidden="true" />{t(($) => $.brand.download)}</Link>
            </div>
            <p className="fact-hero-signature">{t(($) => $.brand.company_english)}</p>
          </div>
          <div className="fact-hero-visual"><FactCollaborationVisual /></div>
        </section>

        <section className="fact-principles-section" id="principles">
          <div className="fact-container">
            <div className="fact-principles-intro">
              <p className="fact-eyebrow">{t(($) => $.brand.principles)}</p>
              <h2>{t(($) => $.brand.principles_title)}</h2>
            </div>
            <div className="fact-principles-grid">
              {principles.map(({ letter, name, title, description }) => (
                <article key={letter}>
                  <div className="fact-principle-mark"><span translate="no">{letter}</span><span translate="no">{name}</span></div>
                  <h3>{title}</h3><p>{description}</p>
                </article>
              ))}
            </div>
            <p className="fact-principles-note">{t(($) => $.brand.principles_note)}</p>
          </div>
        </section>

        <section className="fact-fields fact-container">
          <div><p className="fact-eyebrow">{t(($) => $.brand.fields)}</p><p className="fact-fields-description">{t(($) => $.brand.fields_description)}</p></div>
          <ul>{fields.map((field) => <li key={field}>{field}</li>)}</ul>
        </section>
      </main>
      <footer className="fact-footer fact-container">
        <p>{t(($) => $.brand.company)}</p>
        <p className="fact-footer-mission">{t(($) => $.brand.mission)}</p>
        <a href="https://github.com/dunzic/multica-worker" target="_blank" rel="noopener noreferrer">{t(($) => $.brand.attribution)}<ArrowRight size={14} aria-hidden="true" /></a>
      </footer>
    </div>
  );
}
