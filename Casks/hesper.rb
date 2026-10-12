cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "0.2.0"
  sha256 arm: "013c340fdd57e2c1f964539ddaf5f239bb2f8b3991a51c24a7b9c15ed8d084dd",
         intel: "d654f097e653b8dd5e798e3007a9c119126e6e44207b772b41f25985d7abf8ff"

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
