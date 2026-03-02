{
  pkgs,
  src,
  version,
}:
pkgs.buildNpmPackage {
  pname = "bifrost-ui";
  inherit version;
  inherit src;
  sourceRoot = "source/ui";

  npmDepsHash = "sha256-+tI2NUJtpHwvI9sAYMXO7r00Y3Pb1E62ms1ZSd3O0hM=";

  # Next's `next/font/google` requires network access at build time.
  # Nix builds are sandboxed (no network), so patch the layout to avoid
  # fetching Google Fonts.
  postPatch = ''
    cat > app/layout.tsx <<'EOF'
    import "./globals.css"

    export default function RootLayout({ children }: { children: React.ReactNode }) {
    	return (
    		<html lang="en" suppressHydrationWarning>
    			<head>
    				<link rel="dns-prefetch" href="https://getbifrost.ai" />
    				<link rel="preconnect" href="https://getbifrost.ai" />
    			</head>
    			<body className="font-sans antialiased">{children}</body>
    		</html>
    	)
    }
    EOF
  '';

  # Avoid the upstream build script's copy step (writes outside $PWD).
  npmBuildScript = "build-enterprise";
  env.NEXT_TELEMETRY_DISABLED = "1";
  env.NEXT_DISABLE_ESLINT = "1";

  installPhase = ''
    runHook preInstall

    mkdir -p "$out/ui"
    cp -R --no-preserve=mode,ownership,timestamps out/. "$out/ui/"

    runHook postInstall
  '';
}