import { Switch as S } from "radix-ui";
import type { ComponentProps } from "react";

/**
 * A Radix switch: role="switch", aria-checked, Space toggles, and a hidden
 * input when it sits in a form. Unstyled here: Breaker draws the Fusion
 * toggle (.toggle in fusion.css); other callers pass their own classes.
 */
export function Switch(props: ComponentProps<typeof S.Root>) {
  return <S.Root data-slot="switch" {...props} />;
}

export function SwitchThumb(props: ComponentProps<typeof S.Thumb>) {
  return <S.Thumb data-slot="switch-thumb" {...props} />;
}
