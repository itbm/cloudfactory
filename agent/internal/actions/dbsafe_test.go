package actions

import (
	"io"
	"strings"
	"testing"
)

// The restore of a MariaDB dump removes each definer at the start of a line, and changes no
// data (adversarial review, findings A4-02 and G3-02).
func TestWithoutDefinersRemovesTheDefinersOfADump(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	dump := strings.Join([]string{
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 DEFINER=`acme_x7k2_app`@`169.254.80.1` SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 AS `1` */;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=`acme_x7k2_app`@`169.254.80.1`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.id = 1 */;;",
		"CREATE DEFINER=`acme_x7k2_app`@`169.254.80.1` PROCEDURE `p`()",
		"CREATE DEFINER=`acme_x7k2_app`@`169.254.80.1` FUNCTION `f`() RETURNS int(11)",
		"/*!50106 CREATE*/ /*!50117 DEFINER=`acme_x7k2_app`@`169.254.80.1`*/ /*!50106 EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1 */ ;;",
		"INSERT INTO `posts` VALUES (1,'CREATE DEFINER=`root`@`localhost` PROCEDURE'),(2,'" + long + "');",
		"INSERT INTO `posts` VALUES (3,'" + long + "\\nCREATE DEFINER=`a`@`b` x');",
		"",
	}, "\n")
	want := strings.Join([]string{
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 AS `1` */;",
		"/*!50003 CREATE*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `a` FOR EACH ROW SET NEW.id = 1 */;;",
		"CREATE PROCEDURE `p`()",
		"CREATE FUNCTION `f`() RETURNS int(11)",
		"/*!50106 CREATE*/ /*!50106 EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1 */ ;;",
		"INSERT INTO `posts` VALUES (1,'CREATE DEFINER=`root`@`localhost` PROCEDURE'),(2,'" + long + "');",
		"INSERT INTO `posts` VALUES (3,'" + long + "\\nCREATE DEFINER=`a`@`b` x');",
		"",
	}, "\n")
	got, err := io.ReadAll(withoutDefiners(strings.NewReader(dump)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("the dump with no definers is wrong:\n%.1200s", got)
	}
}

func TestPgSafeSetsTheSearchPathAfterEachConnect(t *testing.T) {
	got := pgSafe("SELECT 1;\n\\connect \"a\"\nSELECT 2;\n")
	want := pgSafePath + "SELECT 1;\n\\connect \"a\"\n" + pgSafePath + "SELECT 2;\n"
	if got != want {
		t.Errorf("pgSafe = %q", got)
	}
}
