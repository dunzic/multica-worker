import { Instrument_Serif } from "next/font/google";
import { LocaleProvider } from "@/features/landing/i18n";
import { getRequestLocale } from "@/lib/request-locale";

// Instrument Serif is the landing display face and is Latin-only. The full
// `--font-serif` stack (Instrument Serif + the per-locale CJK serif tail) is
// composed in static CSS in app/custom.css, not here — same reasoning as
// `--font-sans` in app/globals.css: the CJK tail must be overridable per
// `<html lang>`, and a hashed next/font family can only be referenced from CSS
// through its variable.
const instrumentSerif = Instrument_Serif({
  subsets: ["latin"],
  weight: "400",
  variable: "--font-instrument-serif",
});

const jsonLd = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "Organization",
      name: "智臻纪元智能科技",
      alternateName: "The Fact Technology Company",
    },
    {
      "@type": "SoftwareApplication",
      name: "FACT 数字员工协作平台",
      applicationCategory: "ProjectManagement",
      operatingSystem: "Web",
      description:
        "面向专业服务的人与数字员工协作平台。",
    },
  ],
};

export default async function LandingLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const initialLocale = await getRequestLocale();

  return (
    <>
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{ __html: JSON.stringify(jsonLd) }}
      />
      <div className={`${instrumentSerif.variable} landing-light h-full overflow-x-hidden overflow-y-auto bg-white`}>
        <LocaleProvider initialLocale={initialLocale}>{children}</LocaleProvider>
      </div>
    </>
  );
}
