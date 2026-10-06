// The two Next.js modules vitals.tsx uses, so the SDK type-checks without
// Next installed. Apps resolve the real ones.
declare module "next/navigation" {
  export function usePathname(): string | null;
  export function useParams(): Record<string, string | string[]> | null;
}
declare module "next/web-vitals" {
  export function useReportWebVitals(callback: (metric: { name: string; value: number; id: string }) => void): void;
}
