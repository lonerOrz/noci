{
  lib,
  buildGoModule,
}:

buildGoModule {
  pname = "noci";
  version = "1.0.0";
  src = ../.;

  vendorHash = "sha256-eKeUhS2puz6ALb+cQKl7+DGvm9Cl+miZAHX0imf9wdg=";

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
