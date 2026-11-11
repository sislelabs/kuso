package backup

import (
	"os"
	"strings"
	"testing"

	"kuso/server/internal/testsh"
)

// DB-4: a restore into another addon runs as the destination's role, which
// cannot SET ROLE to the source's owner, so ownership and grants must be
// dropped; everything else, COPY data included, must pass byte-for-byte.
func TestPgOwnershipFilter(t *testing.T) {
	const dump = `--
-- Name: notes; Type: TABLE; Schema: public; Owner: proja_db
--

CREATE TABLE public.notes (
    body text
);


ALTER TABLE public.notes OWNER TO proja_db;

--
-- Data for Name: notes; Type: TABLE DATA; Schema: public; Owner: proja_db
--

COPY public.notes (body) FROM stdin;
GRANT ALL ON TABLE public.notes TO evil;
ALTER TABLE public.notes OWNER TO evil;
\.


--
-- Name: f(); Type: FUNCTION; Schema: public; Owner: proja_db
--

CREATE FUNCTION public.f() RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
GRANT SELECT ON TABLE public.notes TO reader;
END $$;


ALTER FUNCTION public.f() OWNER TO proja_db;

--
-- Name: TABLE notes; Type: ACL; Schema: public; Owner: proja_db
--

GRANT SELECT ON TABLE public.notes TO reader;
REVOKE ALL ON TABLE public.notes FROM PUBLIC;

--
-- Name: DEFAULT PRIVILEGES FOR TABLES; Type: DEFAULT ACL; Schema: -; Owner: proja_db
--

ALTER DEFAULT PRIVILEGES FOR ROLE proja_db GRANT SELECT ON TABLES TO reader;
`
	run := func(strip string) string {
		cmd := testsh.Command(t, pgOwnershipFilterShell+"\npg_ownership_filter")
		cmd.Stdin = strings.NewReader(dump)
		cmd.Env = append(os.Environ(), "RESTORE_STRIP_OWNERSHIP="+strip)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		return string(out)
	}
	if got := run(""); got != dump {
		t.Fatalf("in-place restore must not touch the dump; got:\n%s", got)
	}
	got := run("1")
	for _, gone := range []string{
		"ALTER TABLE public.notes OWNER TO proja_db;",
		"ALTER FUNCTION public.f() OWNER TO proja_db;",
		"GRANT SELECT ON TABLE public.notes TO reader;\nREVOKE",
		"ALTER DEFAULT PRIVILEGES",
	} {
		if strings.Contains(got, gone) {
			t.Errorf("filtered dump still has %q", gone)
		}
	}
	for _, kept := range []string{
		"COPY public.notes (body) FROM stdin;\nGRANT ALL ON TABLE public.notes TO evil;\nALTER TABLE public.notes OWNER TO evil;\n\\.\n",
		"BEGIN\nGRANT SELECT ON TABLE public.notes TO reader;\nEND $$;",
		"CREATE TABLE public.notes (",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("filtered dump lost %q; got:\n%s", kept, got)
		}
	}
}
