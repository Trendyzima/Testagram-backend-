-- Curated starter catalog for Testagram Knowledge topics.
-- Additive and replay-safe: existing topic names/descriptions are never overwritten.
BEGIN;

INSERT INTO testagram_local.topics(slug,name,description)
VALUES
  ('kenya','Kenya','Questions and knowledge about Kenya, its people, places and opportunities.'),
  ('east-africa','East Africa','Regional knowledge, culture, markets and collaboration across East Africa.'),
  ('technology','Technology','Technology products, systems, tools and their real-world impact.'),
  ('software-engineering','Software Engineering','Building, testing, operating and maintaining software systems.'),
  ('artificial-intelligence','Artificial Intelligence','AI research, practical applications, responsible use and engineering.'),
  ('entrepreneurship','Entrepreneurship','Starting, operating and growing sustainable ventures.'),
  ('business','Business','Business strategy, operations, leadership and markets.'),
  ('agriculture','Agriculture','Farming, food systems, agribusiness and agricultural innovation.'),
  ('education','Education','Teaching, learning, research and education systems.'),
  ('science','Science','Scientific discovery, methods, evidence and explanations.'),
  ('climate','Climate and Environment','Climate, conservation, energy and environmental resilience.'),
  ('finance','Finance','Personal finance, financial systems, investing and economic literacy.'),
  ('design','Design','Product, graphic, industrial and experience design.'),
  ('photography','Photography','Photography techniques, visual storytelling and image-making.'),
  ('sports','Sports','Sport, training, competition and sporting communities.'),
  ('music','Music','Music creation, performance, production and culture.'),
  ('film','Film and Television','Film-making, television, criticism and screen culture.'),
  ('travel','Travel','Travel experiences, destinations and practical travel knowledge.'),
  ('history','History','Historical research, events, people and interpretation.'),
  ('creative-writing','Creative Writing','Fiction, poetry, storytelling and the craft of writing.')
ON CONFLICT (slug) DO NOTHING;

COMMIT;
