cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "0.4.0"
  sha256 arm: "1787d7e47a814e532c8ec42eb87a060d3ad65d1c222f4b7f9185bfe06c50c129",
         intel: "8dbe594aa974dd2c353d49b1b850ce2204ffbbada476e7932bf3dd255dc2d3ed"

  url "https://github.com/derzierau/hesper/releases/download/v#{version}/Hesper-v#{version}-#{arch}.zip"
  name "Hesper"
  desc "Run and steer coding agents across your Macs"
  homepage "https://github.com/derzierau/hesper"
  depends_on macos: :sonoma
  container type: :zip

  app "Hesper.app"
  binary "#{appdir}/Hesper.app/Contents/MacOS/hesperctl"
  binary "#{appdir}/Hesper.app/Contents/MacOS/hesperd"
  binary "#{appdir}/Hesper.app/Contents/MacOS/hesper-keys"

  uninstall launchctl: "de.olezierau.hesperd"
  caveats <<~EOS
    Set up the daemon, agent hooks and skill after installing or upgrading:
      /bin/sh "#{appdir}/Hesper.app/Contents/Resources/hesper-setup.sh"
    In Codex, enable the hooks once with /hooks.
  EOS
end
