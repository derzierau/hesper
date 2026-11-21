cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "0.4.2"
  sha256 arm: "6135ad71ae68c287dc5bfed285ce367f77038ad458f1c8ad762d8673fff73103",
         intel: "0b23eb8ddc45a21fb1cd6064d132734ee6cc5870222153eb8cf81ac69add958d"

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
