-- Subconjunto de la tabla `lines` de XUI con las columnas que lee el CDN.
CREATE TABLE `lines` (
  `id` int NOT NULL AUTO_INCREMENT,
  `username` varchar(50) DEFAULT NULL,
  `password` varchar(50) DEFAULT NULL,
  `admin_notes` mediumtext,
  `reseller_notes` mediumtext,
  PRIMARY KEY (`id`)
);

INSERT INTO `lines` (username, password, admin_notes, reseller_notes) VALUES
  ('10000001', 'x', 'C-cliente caracas', NULL),
  ('20000001', 'x', 'V-cliente valencia', NULL),
  ('20000002', 'x', NULL, 'Revendedor VALENCIA norte'),
  ('30000001', 'x', 'A-cliente aragua', NULL),
  ('30000002', 'x', 'M', ''),
  ('30000003', 'x', '', 'margarita'),
  ('40000001', 'x', NULL, NULL);

-- Volumen parecido al real para medir la carga inicial.
SET SESSION cte_max_recursion_depth = 1000000;
INSERT INTO `lines` (username, password, admin_notes, reseller_notes)
WITH RECURSIVE n AS (SELECT 1 AS i UNION ALL SELECT i + 1 FROM n WHERE i < 720000)
SELECT CONCAT('9', LPAD(i, 8, '0')), 'x', ELT(1 + (i % 4), 'C', 'V', 'A', 'M'), NULL FROM n;

CREATE USER 'cdn_lectura'@'%' IDENTIFIED BY 'solo-laboratorio';
GRANT SELECT ON xui.`lines` TO 'cdn_lectura'@'%';
