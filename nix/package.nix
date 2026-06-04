{
  lib,
  buildGoModule,
}:

buildGoModule {
  pname = "noci";
  version = "0-unstable-20260605";
  src = ../.;

  vendorHash = "sha256-bbB1aH4NMZIWp84Tk8W+1ttS3YP2f0fj9tG19u87h8g=";

  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
  ];
  doCheck = false;

  meta = {
    description = "Highly modular Nix binary cache over OCI registry";
    homepage = "https://github.com/lonerOrz/noci";
    license = lib.licenses.bsd3;
    maintainers = with lib.maintainers; [ lonerOrz ];
    mainProgram = "noci";
  };
}
