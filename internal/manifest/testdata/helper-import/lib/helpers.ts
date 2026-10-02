export const appDefaults = { framework: "next", memoryMB: 768 } as const;

export function routeFor(name: string): string {
  return `${name}.example.com`;
}
