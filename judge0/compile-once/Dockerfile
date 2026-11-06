ARG JUDGE0_BASE_IMAGE=judge0/judge0:1.13.1
FROM ${JUDGE0_BASE_IMAGE}
ARG COMPILER_BUILD_ID
RUN test -n "$COMPILER_BUILD_ID"
ENV JUDGE0_COMPILER_BUILD_ID=${COMPILER_BUILD_ID}
COPY lib/compilation_cache.rb /api/lib/compilation_cache.rb
COPY config/initializers/compilation_cache.rb /api/config/initializers/compilation_cache.rb
COPY db/migrate/20261107000000_add_compilation_groups.rb /api/db/migrate/20261107000000_add_compilation_groups.rb
COPY test/*.rb /api/test/
