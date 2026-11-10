# Heroku builds Docker images with the build context fixed to the directory
# that holds the Dockerfile, so the repository Dockerfile (which needs the
# whole tree) cannot be used there. This one-liner makes the Heroku "build" a
# pull of the published image instead. The tag is pinned and bumped by the
# release pipeline (.github/workflows/scripts/bump-deploy-image-tags.sh).
FROM maximhq/bifrost:v2.5.0
