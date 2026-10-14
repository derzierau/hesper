cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "0.3.2"
  sha256 arm: "79931a1ac2c7446d200be4d7e86faf18f6b463dd1b538f872c40803fc9b6eb4c",
         intel: "4054784c866da1d1a3a7ed83d22cbdac63a42e3ef43aca3ac401f31101ecbe22"

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
