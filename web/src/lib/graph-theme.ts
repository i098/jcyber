export interface GraphTheme {
  background: string;
  foreground: string;
  card: string;
  muted: string;
  mutedForeground: string;
  ring: string;
}

// G6's color parser does not understand the app's OKLCH CSS tokens.
// Let the browser convert them to sRGB before passing colors to the canvas.
export function readGraphTheme(): GraphTheme {
  const style = getComputedStyle(document.documentElement);
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 1;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  if (!context) throw new Error("A 2D canvas context is required for the asset graph");

  const color = (token: string) => {
    context.clearRect(0, 0, 1, 1);
    context.fillStyle = style.getPropertyValue(token).trim();
    context.fillRect(0, 0, 1, 1);
    const [r, g, b, a] = context.getImageData(0, 0, 1, 1).data;
    return `rgba(${r}, ${g}, ${b}, ${a / 255})`;
  };

  return {
    background: color("--background"),
    foreground: color("--foreground"),
    card: color("--card"),
    muted: color("--muted"),
    mutedForeground: color("--muted-foreground"),
    ring: color("--ring"),
  };
}

export function observeGraphTheme(onChange: () => void) {
  // The preference provider applies explicit and system themes to this element.
  const root = document.documentElement;
  let dark = root.classList.contains("dark");
  let preset = root.dataset.themePreset;
  const observer = new MutationObserver(() => {
    const nextDark = root.classList.contains("dark");
    const nextPreset = root.dataset.themePreset;
    if (nextDark === dark && nextPreset === preset) return;
    dark = nextDark;
    preset = nextPreset;
    onChange();
  });
  observer.observe(root, {
    attributes: true,
    attributeFilter: ["class", "data-theme-preset"],
  });
  return () => observer.disconnect();
}
