import type { ReactNode } from "react";

export function Toggle({ checked, disabled, onChange, children }: {
  checked: boolean; disabled?: boolean; onChange: (checked: boolean) => void; children: ReactNode;
}) {
  return <label className="settings-toggle">
    <span>{children}</span>
    <span className="switch">
      <input type="checkbox" role="switch" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
      <span aria-hidden="true" />
    </span>
  </label>;
}
