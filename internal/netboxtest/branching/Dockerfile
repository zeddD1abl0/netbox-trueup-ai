# NetBox with netbox-branching, for the test NetBox's "branching" profile
# only (../README.md). Built on this machine by Docker Compose and by
# netboxtest, never pushed to a registry.
#
# The base must be the stock netbox service's image in ../compose.yaml,
# exactly; TestComposeFile checks. netboxtest builds this with Docker's
# classic builder and Compose with BuildKit, so it uses only instructions
# both build the same way: no ARG, no syntax directive, no RUN --mount.
FROM docker.io/netboxcommunity/netbox:v4.7.2-5.1.1@sha256:3cf9d3bd77aa186233b84d0ea1cf1755581d5e5678fa7d86ec3be62f79d0c643

# Only the wheel whose hash requirements.txt pins, without dependencies (it
# has none), so that installing it runs no build code.
COPY requirements.txt /opt/netbox/branching-requirements.txt
RUN ["/usr/local/bin/uv", "pip", "install", "--require-hashes", "--only-binary", ":all:", "--no-deps", "--requirement", "/opt/netbox/branching-requirements.txt"]

# netbox-docker reads every *.py in /etc/netbox/config; this replaces its
# empty plugins.py.
COPY plugins.py /etc/netbox/config/plugins.py

# COPY keeps the modes the files have in the checkout under BuildKit, which
# follow the umask of whoever cloned it, and sets 0644 under the classic
# builder. Set them here, so that NetBox, which runs as netbox:root, can read
# its configuration, and can't change it, whichever built the image.
RUN ["chmod", "0644", "/etc/netbox/config/plugins.py", "/opt/netbox/branching-requirements.txt"]
