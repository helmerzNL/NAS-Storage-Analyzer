"""Where the data lives and which image runs (baseline sections 20 and 21).

The database is the folder `DATA_PATH/postgres` of the host, mounted by the db service only. Nothing is a Docker
volume: `docker compose down -v` or a prune must not be able to touch it. One variable, NASA_TAG, picks the image and
is passed into the container so a beta build can say so. These tests read the Compose file and `.env.example` as
text and fail when either drifts, and drive `init-env.sh` and `migrate_data.sh` with a fake `docker`.

Run: python -m unittest discover -s scripts -p "test_deploy_layout.py" -v
"""
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
ENV_EXAMPLE = (ROOT / ".env.example").read_text(encoding="utf-8")


def service_block(name: str) -> str:
    return re.search(rf"^  {name}:\n(.*?)(?=^  \w|^\w|\Z)", COMPOSE, re.S | re.M).group(1)


def short_mounts(block: str) -> list[str]:
    """The `- host:container` lines of a service's volumes, comments removed."""
    volumes = re.search(r"^    volumes:\n((?:      .*\n)+)", block, re.M)
    if not volumes:
        return []
    lines = [re.sub(r"\s+#.*$", "", ln.strip()) for ln in volumes.group(1).splitlines() if not ln.strip().startswith("#")]
    return [ln[2:] for ln in lines if ln.startswith("- ")]


def env_value(text: str, name: str) -> str:
    return re.search(rf"^{name}=(.*)$", text, re.M).group(1)


class ComposeLayout(unittest.TestCase):
    def test_the_database_is_a_folder_under_data_path_that_only_the_db_service_mounts(self):
        self.assertEqual(short_mounts(service_block("db")), ["${DATA_PATH}/postgres:/var/lib/postgresql/data"])
        self.assertNotIn("DATA_PATH}", "".join(short_mounts(service_block("app")) + short_mounts(service_block("worker"))))

    def test_nothing_in_the_compose_file_is_a_docker_volume(self):
        self.assertIsNone(re.search(r"^volumes:", COMPOSE, re.M), "a named volume is declared")
        named = re.findall(r"^\s+- ([A-Za-z][\w.-]*):/", COMPOSE, re.M)
        self.assertEqual(named, [], f"named volume mounts: {named}")
        for src in re.findall(r"^\s+source: (\S+)", COMPOSE, re.M):
            self.assertTrue(src.startswith("${NASA_SHARES_DIR"), f"a bind mount outside the shares folder: {src}")

    def test_data_path_and_the_shares_folder_are_required(self):
        """Compose refuses to start without them; DATA_PATH also reaches the containers so they can warn."""
        self.assertIn("DATA_PATH: ${DATA_PATH:?", COMPOSE)
        self.assertIn("source: ${NASA_SHARES_DIR:?", COMPOSE)

    def test_a_new_installation_gets_absolute_folders(self):
        """./data is resolved by whatever tool starts the stack (Dockhand on Unraid: a RAM disk), so no template hands it out."""
        for name in ("DATA_PATH", "NASA_SHARES_DIR"):
            self.assertTrue(env_value(ENV_EXAMPLE, name).startswith("/"), f"{name} in .env.example is not absolute")

    def test_one_tag_picks_the_image_and_tells_the_container_which_channel_it_is(self):
        self.assertIn("image: ghcr.io/helmerznl/nas-storage-analyzer:${NASA_TAG:-latest}", COMPOSE)
        self.assertIn("NASA_TAG: ${NASA_TAG:-latest}", COMPOSE)
        self.assertEqual(env_value(ENV_EXAMPLE, "NASA_TAG"), "latest")  # stable unless chosen otherwise
        self.assertIn("separate DATA_PATH for beta", ENV_EXAMPLE)
        self.assertNotRegex(COMPOSE + ENV_EXAMPLE, r"NASA_IMAGE")  # no second variable that names the whole image
        self.assertNotIn("build:", COMPOSE)  # the pull is the default; building is the override

    def test_every_variable_the_compose_file_reads_is_in_the_env_example(self):
        used = set(re.findall(r"\$\{([A-Z][A-Z0-9_]*)", COMPOSE)) - {"POSTGRES_USER", "POSTGRES_DB"}  # the last two are the db container's own, escaped as $${..}
        for name in sorted(used):
            self.assertRegex(ENV_EXAMPLE, rf"(?m)^#?{name}=", f"{name} is read by docker-compose.yml but not documented in .env.example")

    def test_the_build_override_builds_both_images_with_version_and_commit(self):
        build = (ROOT / "docker-compose.build.yml").read_text(encoding="utf-8")
        self.assertRegex(build, r"(?m)^  app:\n    build: \*build")
        self.assertRegex(build, r"(?m)^  worker:\n    build: \*build")
        self.assertIn("GIT_COMMIT: ${NASA_COMMIT:-}", build)
        self.assertIn("ARG GIT_COMMIT", (ROOT / "Dockerfile").read_text(encoding="utf-8"))

    def test_the_shell_scripts_parse(self):
        for script in ("scripts/init-env.sh", "scripts/migrate_data.sh"):
            self.assertEqual(subprocess.run(["sh", "-n", str(ROOT / script)]).returncode, 0, script)


class InitEnv(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        (self.tmp / "scripts").mkdir()
        shutil.copy(ROOT / "scripts" / "init-env.sh", self.tmp / "scripts" / "init-env.sh")

    def run_script(self, *args):
        return subprocess.run(["sh", str(self.tmp / "scripts" / "init-env.sh"), *args], capture_output=True, text=True)

    def test_a_relative_folder_is_refused_and_nothing_is_written(self):
        for args in (["--data-path", "./data"], ["--data-path", "data"], ["--shares-dir", "shares"], ["--data-path=./x"]):
            done = self.run_script(*args)
            self.assertNotEqual(done.returncode, 0, args)
            self.assertIn("absolute", done.stderr)
            self.assertFalse((self.tmp / ".env").exists(), args)

    def test_absolute_folders_are_written_for_both_modes(self):
        done = self.run_script("--data-path", "/srv/nasa-data", "--shares-dir", "/mnt/shares-on-host")
        self.assertEqual(done.returncode, 0, done.stderr)
        env = (self.tmp / ".env").read_text(encoding="utf-8")
        self.assertEqual(env_value(env, "DATA_PATH"), "/srv/nasa-data")
        self.assertEqual(env_value(env, "NASA_SHARES_DIR"), "/mnt/shares-on-host")
        self.assertEqual(env_value(env, "NASA_TAG"), "latest")
        self.assertGreater(len(env_value(env, "NASA_DB_PASSWORD")), 20)
        (self.tmp / ".env").unlink()
        done = self.run_script("--secrets", "--data-path", "/srv/nasa-data", "--shares-dir", "/mnt/shares-on-host")
        self.assertEqual(done.returncode, 0, done.stderr)
        env = (self.tmp / ".env").read_text(encoding="utf-8")
        self.assertEqual(env_value(env, "DATA_PATH"), "/srv/nasa-data")  # the secrets variant needs both too, or compose refuses it
        self.assertNotIn("NASA_DB_PASSWORD", env)

    def test_the_default_is_absolute_too(self):
        done = self.run_script()
        self.assertEqual(done.returncode, 0, done.stderr)
        env = (self.tmp / ".env").read_text(encoding="utf-8")
        self.assertTrue(env_value(env, "DATA_PATH").startswith("/"))
        self.assertTrue(env_value(env, "NASA_SHARES_DIR").startswith("/"))

    def test_an_existing_env_is_never_overwritten(self):
        (self.tmp / ".env").write_text("KEEP=me\n", encoding="utf-8")
        self.assertNotEqual(self.run_script("--data-path", "/srv/x").returncode, 0)
        self.assertEqual((self.tmp / ".env").read_text(encoding="utf-8"), "KEEP=me\n")


FAKE_DOCKER = """#!/bin/sh
# Answers like docker would for one stack: FAKE_MOUNT is what `docker inspect` reports for the db container's data mount
# (source|type|name; empty means there is no db container), FAKE_VOLUME the mountpoint of the old db-data volume (empty: none).
echo "$*" >> "$FAKE_LOG"
case "$*" in
  "ps -aq --filter label=com.docker.compose.project=nas-storage-analyzer --filter label=com.docker.compose.service=db"|"ps -aq --filter label=com.docker.compose.project=other --filter label=com.docker.compose.service=db")
    [ -z "$FAKE_MOUNT" ] || echo abc123 ;;
  "ps -q --filter label=com.docker.compose.project="*)
    [ -z "$FAKE_MOUNT" ] || printf 'abc123\\ndef456\\n' ;;
  "inspect "*) echo "$FAKE_MOUNT" ;;
  "volume inspect "*) [ -n "$FAKE_VOLUME" ] || exit 1; echo "$FAKE_VOLUME" ;;
  "stop "*) ;;
  *) echo "fake docker: unexpected call: $*" >&2; exit 99 ;;
esac
"""


class MigrateData(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        (self.bin / "docker").write_text(FAKE_DOCKER, encoding="utf-8")
        (self.bin / "docker").chmod(0o755)
        self.log = self.tmp / "log"
        self.work = self.tmp / "stack"
        self.work.mkdir()
        self.data = self.tmp / "array" / "nasa"

    def database(self, folder: Path) -> Path:
        """A PostgreSQL data folder as the db container leaves it."""
        (folder / "base").mkdir(parents=True)
        (folder / "PG_VERSION").write_text("16\n")
        (folder / "base" / "16384").write_bytes(b"rows" * 100)
        os.utime(folder / "PG_VERSION", (1_500_000_000, 1_500_000_000))
        if os.geteuid() == 0:
            for path in (folder, folder / "PG_VERSION", folder / "base", folder / "base" / "16384"):
                os.chown(path, 70, 70)  # the postgres user of the alpine image
        folder.chmod(0o700)
        return folder

    def volume(self) -> Path:
        return self.database(self.tmp / "docker" / "volumes" / "nas-storage-analyzer_db-data" / "_data")

    def run_script(self, mount="", volume="", *args, env_file=None, extra_path=None):
        if env_file is not None:
            (self.work / ".env").write_text(env_file, encoding="utf-8")
        env = {"PATH": f"{self.bin}:/usr/bin:/bin", "FAKE_MOUNT": str(mount), "FAKE_VOLUME": str(volume), "FAKE_LOG": str(self.log)}
        done = subprocess.run(["sh", str(ROOT / "scripts" / "migrate_data.sh"), *args], cwd=self.work, env=env, capture_output=True, text=True)
        return done, (self.log.read_text() if self.log.exists() else "")

    def stopped(self, log: str) -> bool:
        return "stop abc123 def456" in log

    def test_a_database_in_the_old_volume_is_copied_to_data_path_and_the_volume_stays(self):
        vol = self.volume()
        done, log = self.run_script(f"{vol}|volume|nas-storage-analyzer_db-data", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual((self.data / "postgres" / "PG_VERSION").read_text(), "16\n")
        self.assertEqual((self.data / "postgres" / "base" / "16384").read_bytes(), b"rows" * 100)
        self.assertEqual(oct((self.data / "postgres").stat().st_mode & 0o777), "0o700")  # PostgreSQL refuses a data folder that others can read
        copied = self.data / "postgres" / "PG_VERSION"
        self.assertEqual(int(copied.stat().st_mtime), 1_500_000_000, "cp -a keeps timestamps; a plain copy does not")
        if os.geteuid() == 0:  # only root can hand files to another user, and only root can read a Docker volume anyway
            self.assertEqual((copied.stat().st_uid, (self.data / "postgres").stat().st_uid), (70, 70), "PostgreSQL needs its own user to own its data")
        self.assertTrue((vol / "PG_VERSION").exists(), "the old volume must stay")
        self.assertTrue(self.stopped(log), "the whole stack is stopped before the copy")
        self.assertIn("docker volume rm nas-storage-analyzer_db-data", done.stdout)
        self.assertFalse((self.data / ".postgres.copying").exists())

    def test_a_database_in_another_folder_is_copied_and_the_old_folder_stays(self):
        old = self.database(self.tmp / "ram" / "data" / "postgres")
        done, _ = self.run_script(f"{old}|bind|", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertTrue((self.data / "postgres" / "PG_VERSION").exists() and (old / "PG_VERSION").exists())
        self.assertIn(f"rm -rf '{old}'", done.stdout)

    def test_data_that_is_already_in_place_is_left_alone(self):
        self.database(self.data / "postgres")
        done, log = self.run_script(f"{self.data}/postgres|bind|", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0)
        self.assertIn("already in", done.stdout)
        self.assertFalse(self.stopped(log))
        self.assertEqual(done.stderr, "")

    def test_running_it_again_after_the_move_does_nothing(self):
        vol = self.volume()
        self.assertEqual(self.run_script(f"{vol}|volume|x", "", env_file=f"DATA_PATH={self.data}\n")[0].returncode, 0)
        self.log.unlink()
        done, log = self.run_script(f"{self.data}/postgres|bind|", "", env_file=f"DATA_PATH={self.data}\n")  # docker now reports the new folder
        self.assertEqual(done.returncode, 0)
        self.assertIn("already in", done.stdout)
        self.assertFalse(self.stopped(log))

    def test_a_new_stack_that_started_empty_next_to_the_old_volume_is_called_out(self):
        """The new compose file was deployed before the script ran: the db started on an empty folder and the data is still in the volume."""
        self.database(self.data / "postgres")
        vol = self.volume()
        done, log = self.run_script(f"{self.data}/postgres|bind|", vol, env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0)
        self.assertIn("nas-storage-analyzer_db-data still exists", done.stderr)
        self.assertIn("setup token", done.stderr)
        self.assertIn("--from-volume", done.stderr)
        self.assertFalse(self.stopped(log))

    def test_from_volume_brings_the_old_data_back_after_the_new_stack_started_empty(self):
        """The way back: stop, move the empty database away, copy the old volume in."""
        vol = self.volume()
        fresh = self.database(self.data / "postgres")
        (fresh / "base" / "16384").write_bytes(b"empty")
        fresh.rename(self.data / "postgres.empty")  # moved away, not deleted
        done, log = self.run_script(f"{self.data}/postgres|bind|", vol, "--from-volume", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual((self.data / "postgres" / "base" / "16384").read_bytes(), b"rows" * 100)
        self.assertEqual((self.data / "postgres.empty" / "base" / "16384").read_bytes(), b"empty")
        self.assertTrue((vol / "PG_VERSION").exists())
        self.assertTrue(self.stopped(log))

    def test_from_volume_does_not_overwrite_a_database_in_data_path(self):
        vol = self.volume()
        mine = self.database(self.data / "postgres")
        (mine / "base" / "16384").write_bytes(b"new rows written since")  # a database that differs from the volume
        done, log = self.run_script(f"{self.data}/postgres|bind|", vol, "--from-volume", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 1)
        self.assertIn("Not choosing between them", done.stderr)
        self.assertTrue((mine / "PG_VERSION").exists())
        self.assertFalse(self.stopped(log))

    def test_from_volume_without_a_volume_is_an_error(self):
        done, _ = self.run_script("", "", "--from-volume", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 1)
        self.assertIn("no Docker volume", done.stderr)

    def test_running_it_twice_before_deploying_finds_its_own_copy_and_does_nothing(self):
        vol = self.volume()
        self.assertEqual(self.run_script(f"{vol}|volume|x", "", env_file=f"DATA_PATH={self.data}\n")[0].returncode, 0)
        self.log.unlink()
        done, log = self.run_script(f"{vol}|volume|x", "", env_file=f"DATA_PATH={self.data}\n")  # docker still reports the volume
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertIn("already an exact copy", done.stdout)
        self.assertFalse(self.stopped(log))

    def test_it_never_chooses_between_two_databases(self):
        vol = self.volume()
        mine = self.database(self.data / "postgres")
        (mine / "base" / "16384").write_bytes(b"new")
        done, log = self.run_script(f"{vol}|volume|x", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 1)
        self.assertIn("Not choosing between them", done.stderr)
        self.assertEqual((mine / "base" / "16384").read_bytes(), b"new")  # untouched
        self.assertFalse(self.stopped(log))

    def test_check_changes_nothing(self):
        vol = self.volume()
        done, log = self.run_script(f"{vol}|volume|x", "", "--check", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0)
        self.assertIn("--check: would stop the stack and copy", done.stdout)
        self.assertFalse(self.data.exists())
        self.assertFalse(self.stopped(log))

    def test_a_relative_or_missing_data_path_is_refused(self):
        vol = self.volume()
        for env_file, args in ((f"DATA_PATH=./data\n", ()), ("OTHER=1\n", ()), ("", ("--data-path", "data"))):
            done, log = self.run_script(f"{vol}|volume|x", "", *args, env_file=env_file)
            self.assertEqual(done.returncode, 1, (env_file, args))
            self.assertRegex(done.stderr, "relative path|not set")
            self.assertEqual(log, "", "docker must not be called with a bad DATA_PATH")

    def test_data_path_can_come_from_the_option_or_the_environment(self):
        vol = self.volume()
        done, _ = self.run_script(f"{vol}|volume|x", "", "--data-path", str(self.data), env_file="")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertTrue((self.data / "postgres" / "PG_VERSION").exists())

    def test_no_db_container_means_nothing_to_move(self):
        done, log = self.run_script("", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0)
        self.assertIn("nothing to move", done.stdout)
        self.assertFalse(self.stopped(log))
        self.assertFalse(self.data.exists())

    def test_another_project_name_is_found_through_the_option(self):
        done, log = self.run_script("", "", "--project", "other", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 0)
        self.assertIn("project=other", log)

    def test_a_copy_that_differs_is_not_moved_into_place(self):
        vol = self.volume()
        # A cp that damages what it copies, as a full disk or a failing one would.
        real_cp = shutil.which("cp")
        (self.bin / "cp").write_text(f'#!/bin/sh\n{real_cp} "$@" || exit $?\nfor last; do :; done\nprintf x >> "$last/base/16384"\n', encoding="utf-8")
        (self.bin / "cp").chmod(0o755)
        done, _ = self.run_script(f"{vol}|volume|x", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 1)
        self.assertIn("differs", done.stderr)
        self.assertFalse((self.data / "postgres").exists(), "a copy that was not verified must not look like a database")
        self.assertEqual((vol / "base" / "16384").read_bytes(), b"rows" * 100)

    def test_an_unfinished_copy_is_not_overwritten_blindly(self):
        vol = self.volume()
        (self.data / ".postgres.copying").mkdir(parents=True)
        done, _ = self.run_script(f"{vol}|volume|x", "", env_file=f"DATA_PATH={self.data}\n")
        self.assertEqual(done.returncode, 1)
        self.assertIn("left over", done.stderr)


if __name__ == "__main__":
    unittest.main()
