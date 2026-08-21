# ONE base image for every machine in every topology, parameterised by build arguments.
#
# Spec §4.2: "One base image with build arguments beats one Dockerfile per machine; adding a
# case must be one table row, or the harness becomes a second product to maintain."
#
# Two things this image deliberately does NOT do:
#
#   1. It does not generate or carry host keys. A key baked in here would be COPIED into every
#      container, every machine would present one identity, and the derived graph would
#      collapse to a single node — the fixture would then encode the belief that a fleet is a
#      fleet (spec §2.3). Host keys are generated per container at start-up, by the command the
#      generator writes into the compose file, and `harness/gen`'s tests assert both halves.
#   2. It has no usable default command. sshd refuses to start without host keys, so an image
#      run outside the generated compose fails — and it fails with a sentence naming the reason
#      rather than with sshd's own. That is the structural half of rule 1: there is no way to
#      make this image work standalone except by baking a key, which is the thing forbidden.

ARG BASE=ubuntu:22.04
FROM ${BASE}

# `posix` or `nushell`. A non-POSIX login shell made a whole host invisible on 2026-08-20, so
# it is a first-class row of the topology rather than a variant added later.
ARG SHELL_KIND=posix

# The tmux version this machine must run, or empty for a machine with no tmux at all — which is
# a real case, since §3.1 lists TmuxVersion as "what `tmux -V` answered, or absent".
#
# The version comes from the DISTRIBUTION, never from source: the plan's ruling is packaged tmux
# only, because 3.7b is newer than any distribution and the requirement is heterogeneity. The
# build ASSERTS what it got, so the day a distribution moves off the declared version the build
# fails loudly instead of the fixture quietly claiming a version it is not running.
ARG TMUX=""

# nushell is packaged by neither Debian nor Ubuntu — checked 2026-08-20 against
# sources.debian.org (404 for `nushell`, 11 versions for `fish` as the control) and against
# Launchpad (0 published sources) — so it comes from the project's own release, pinned by
# version AND by digest.
ARG NU_VERSION=0.115.0
ARG NU_SHA256_AMD64=d510565b039b5384986652e579fe443877fcc4df86f16072ed5edca641ad92f1
ARG NU_SHA256_ARM64=7e60824f66b8c814fdbb52741047d38c8379e4bc5e8271135667ef04e3c55324

ENV DEBIAN_FRONTEND=noninteractive

# iproute2 carries both `tc` (the per-edge delay) and `ss` (the healthcheck). openssh-client is
# needed as well as the server, because a hop is an OBSERVER: it runs `ssh -G` and opens
# connections of its own, which is the whole mechanism discovery reads. jq is what lets the fake
# `claude` answer `--all` differently from bare `--json`, which is a measured difference.
#
# The `rm` is the load-bearing line, and it is not tidiness. MEASURED 2026-08-20 on debian:13:
# installing `openssh-server` runs a postinst that calls `ssh-keygen -A`, so the image layer ends
# up holding `ssh_host_{ecdsa,ed25519,rsa}_key` — three real keys with real fingerprints. And
# `ssh-keygen -A` only creates keys that are MISSING, so the start-up would leave them alone and
# every container built from this image would present ONE identity. That is the single-node
# collapse spec §2.3 is about, arriving through the package manager rather than through a `COPY`,
# where no reading of this file would have found it.
#
# Deleting them here makes the start-up's `ssh-keygen -A` the only writer, and the start-up also
# REFUSES if it finds a key already present — so the failure is a fleet that will not come up
# rather than a fleet that is secretly one machine.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server openssh-client iproute2 ca-certificates curl jq \
 && rm -rf /var/lib/apt/lists/* \
 && rm -f /etc/ssh/ssh_host_*

RUN if [ -n "$TMUX" ]; then \
      apt-get update \
      && apt-get install -y --no-install-recommends tmux \
      && rm -rf /var/lib/apt/lists/* \
      && got="$(tmux -V)" \
      && case "$got" in \
           "tmux $TMUX"*) echo "tmux $TMUX confirmed: $got" ;; \
           *) echo "the topology declares tmux $TMUX and this base packages $got — the ruling is packaged tmux only, so either change the topology's version or add a base image for it" >&2; exit 1 ;; \
         esac ; \
    fi

RUN if [ "$SHELL_KIND" = "nushell" ]; then \
      arch="$(dpkg --print-architecture)" ; \
      case "$arch" in \
        amd64) target=x86_64 ; sha="$NU_SHA256_AMD64" ;; \
        arm64) target=aarch64 ; sha="$NU_SHA256_ARM64" ;; \
        *) echo "no pinned nushell for $arch; add its digest beside the other two" >&2; exit 1 ;; \
      esac ; \
      url="https://github.com/nushell/nushell/releases/download/${NU_VERSION}/nu-${NU_VERSION}-${target}-unknown-linux-musl.tar.gz" ; \
      curl -fsSL -o /tmp/nu.tar.gz "$url" ; \
      echo "$sha  /tmp/nu.tar.gz" | sha256sum -c - ; \
      mkdir -p /tmp/nu && tar -xzf /tmp/nu.tar.gz -C /tmp/nu ; \
      install -m 0755 "$(find /tmp/nu -maxdepth 2 -type f -name nu | head -1)" /usr/local/bin/nu ; \
      rm -rf /tmp/nu /tmp/nu.tar.gz ; \
      echo /usr/local/bin/nu >> /etc/shells ; \
    fi

# uid 1000 mirrors the live fleet, where the remote uid decides both the tmux socket path and
# which `~/.claude` is read. The login shell is set HERE because it is the machine's own
# property; everything that differs per RUN is in the generated compose command instead.
RUN useradd --create-home --uid 1000 --shell /bin/sh dev \
 && if [ "$SHELL_KIND" = "nushell" ]; then usermod --shell /usr/local/bin/nu dev ; fi \
 && passwd -l dev

# The fake `claude`, replaying JSON captured from the real CLI on 2026-08-20 and reduced to two
# sessions. Never hand-written: the shape — which fields are present on which record — is the
# thing every consumer reads, and this repository has twice paid for an invented fixture.
COPY claude-agents.json /harness/claude-agents.json
COPY claude /usr/local/bin/claude

RUN chmod 0755 /usr/local/bin/claude

CMD ["/bin/sh", "-c", "echo 'this image has no host keys: it must be started by the command harness/gen writes into the compose file, which generates them per container. Run: go run ./harness/gen && docker compose -f harness/.out/compose.yaml up -d --build --wait' >&2; exit 64"]
