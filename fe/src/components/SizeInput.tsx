import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

// The backend (Incus config value format) accepts any of these suffixes,
// but only the ones actually useful for sizing a single VM's memory/disk
// are offered here — no one is sizing a VM in petabytes.
const SIZE_UNITS = ["MB", "GB", "MiB", "GiB"] as const;
const DEFAULT_UNIT = "GiB";
const SIZE_VALUE_RE =
  /^(\d+)(B|kB|MB|GB|TB|PB|EB|KiB|MiB|GiB|TiB|PiB|EiB)$/;

function parseSize(value: string): { amount: string; unit: string } {
  const match = SIZE_VALUE_RE.exec(value);
  if (!match) return { amount: "", unit: DEFAULT_UNIT };
  return { amount: match[1], unit: match[2] };
}

interface SizeInputProps {
  value: string;
  onChange: (value: string) => void;
  onBlur?: () => void;
  disabled?: boolean;
  placeholder?: string;
  name?: string;
}

// Combines a plain numeric amount with a unit dropdown into the single
// "<number><unit>" string (e.g. "2GiB") the API expects — previously a
// single free-text field where the user had to type the unit suffix
// themselves and get it exactly right.
export function SizeInput({
  value,
  onChange,
  onBlur,
  disabled,
  placeholder,
  name,
}: SizeInputProps) {
  const { amount, unit } = parseSize(value);
  // A value using a unit we don't offer (shouldn't happen via this
  // component, but guards against e.g. a value seeded from elsewhere)
  // still displays sensibly rather than silently reverting the amount.
  const displayUnit = (SIZE_UNITS as readonly string[]).includes(unit)
    ? unit
    : DEFAULT_UNIT;

  function emit(nextAmount: string, nextUnit: string) {
    onChange(nextAmount ? `${nextAmount}${nextUnit}` : "");
  }

  return (
    <div className="flex gap-2">
      <Input
        type="text"
        inputMode="numeric"
        name={name}
        placeholder={placeholder}
        disabled={disabled}
        value={amount}
        onChange={(e) => emit(e.target.value.replace(/\D/g, ""), displayUnit)}
        onBlur={onBlur}
        className="flex-1"
      />
      <Select
        value={displayUnit}
        onValueChange={(nextUnit) => emit(amount, nextUnit)}
        disabled={disabled}
      >
        <SelectTrigger className="w-24">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper">
          {SIZE_UNITS.map((u) => (
            <SelectItem key={u} value={u}>
              {u}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
